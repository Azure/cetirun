#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Qualify a local Hermes image with two synthetic HOMEs and a loopback browser.

Requires Docker, Helm, Node, OpenSSL, PyYAML, Playwright and a prepared Chromium.
All images must already exist locally. Containers have no external network;
their model endpoint is synthetic. Evidence is retained; owned containers,
proxies and networks are removed on exit. This does not access a cluster.
"""

import argparse
import base64
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import yaml

REPO = Path(__file__).resolve().parents[2]
SCRIPTS = Path(__file__).resolve().parent


def command(*args, **kwargs):
    return subprocess.run(list(map(str, args)), check=True, **kwargs)


def capture(*args, **kwargs):
    return subprocess.check_output(list(map(str, args)), text=True, **kwargs).strip()


def fixture(args, out):
    image = args.image
    image_identity = capture('docker', 'image', 'inspect', image, '--format', '{{.Id}}')
    network = 'cetirun-webapp-' + out.name.rsplit('-', 1)[-1]
    containers = []
    command('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
            '-subj', '/CN=localhost', '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost',
            '-keyout', out / 'key.pem', '-out', out / 'cert.pem', stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    (out / 'backends.json').write_text('{}')
    proxy = subprocess.Popen(['node', str(SCRIPTS / 'hermes_webapp_proxy.cjs'), str(out)], stdout=subprocess.PIPE, text=True)
    port = int(proxy.stdout.readline().strip())
    origin = f'https://127.0.0.1:{port}'
    command('docker', 'network', 'create', '--internal', network, stdout=subprocess.DEVNULL)
    gateway = json.loads(capture('docker', 'network', 'inspect', network))[0]['IPAM']['Config'][0]['Gateway']
    model_requests = []

    class Model(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            if self.path != '/v1/models':
                self.send_error(404)
                return
            body = json.dumps({'object': 'list', 'data': [{'id': 'synthetic-model', 'object': 'model', 'owned_by': 'fixture'}]}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            allowed = (self.path == '/v1/chat/completions' and request.get('model') == 'synthetic-model'
                       and self.headers.get('Authorization') == 'Bearer synthetic-model-key')
            model_requests.append({'path': self.path, 'model': request.get('model'),
                                   'stream': bool(request.get('stream')),
                                   'roles': [message.get('role') for message in request.get('messages', [])],
                                   'status': 200 if allowed else 404})
            (out / 'model-requests.json').write_text(json.dumps(model_requests, indent=2))
            if not allowed:
                self.send_error(404)
                return
            html = '<!doctype html>\n<html><head><title>Hermes fixture artifact</title></head><body><main><h1>Hermes fixture artifact</h1><p>This synthetic page checks browser rendering and isolation without an external model.</p><button id="count">Count: 0</button><script>let count=0;document.getElementById("count").onclick=()=>{document.getElementById("count").textContent="Count: "+(++count)}</script></main></body></html>'
            text = 'Here is the synthetic workspace artifact.\n\n```html\n' + html + '\n```\n'
            result = {'id': 'synthetic-response', 'model': request.get('model'), 'created': 1,
                      'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': text}, 'finish_reason': 'stop'}],
                      'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}, 'object': 'chat.completion'}
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream' if request.get('stream') else 'application/json')
            self.end_headers()
            if request.get('stream'):
                chunk = {'id': result['id'], 'model': result['model'], 'object': 'chat.completion.chunk', 'created': 1,
                         'choices': [{'index': 0, 'delta': {'role': 'assistant', 'content': text}, 'finish_reason': None}]}
                self.wfile.write(('data: ' + json.dumps(chunk) + '\n\n').encode())
                chunk['choices'] = [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}]
                chunk['usage'] = result['usage']
                self.wfile.write(('data: ' + json.dumps(chunk) + '\n\ndata: [DONE]\n\n').encode())
            else:
                self.wfile.write(json.dumps(result).encode())

    model = ThreadingHTTPServer((gateway, 0), Model)
    model_thread = threading.Thread(target=model.serve_forever, daemon=True)
    model_thread.start()
    backends, instances = {}, []
    try:
        for name in ('a', 'b'):
            print('Starting synthetic instance ' + name, flush=True)
            directory = out / name
            directory.mkdir(mode=0o755)
            prefix = '/ui/' + name
            values = {
                'agentType': 'hermes', 'user': {'alias': 'browser-' + name},
                'desktop': {'enabled': False}, 'memoryContext': {'enabled': False},
                'skills': {'shared': False, 'npm': []}, 'sessionLogger': {'enabled': False},
                'secrets': {'model': 'synthetic-model', 'apiBase': f'http://{gateway}:{model.server_port}/v1'},
                'aiGateway': {'keySecretName': 'synthetic-model-key'},
                'hermes': {'env': {'HERMES_WEBAPP_PUBLIC_URL': origin + prefix, 'API_SERVER_CORS_ORIGINS': origin}},
            }
            (directory / 'values.yaml').write_text(yaml.safe_dump(values))
            rendered = capture(args.helm, 'template', 'browser-' + name, REPO / 'app/cetirun-chart', '-f', directory / 'values.yaml')
            objects = list(yaml.safe_load_all(rendered))
            pod = next(obj for obj in objects if obj and obj['kind'] == 'StatefulSet')['spec']['template']['spec']
            maps = {obj['metadata']['name']: obj['data'] for obj in objects if obj and obj['kind'] == 'ConfigMap'}
            secrets = {obj['metadata']['name']: {key: base64.b64decode(value).decode() for key, value in obj['data'].items()}
                       for obj in objects if obj and obj['kind'] == 'Secret'}
            secrets['synthetic-model-key'] = {'api-key': 'synthetic-model-key'}
            storage = {}
            for volume in pod['volumes']:
                target = directory / volume['name']
                target.mkdir(mode=0o755)
                storage[volume['name']] = target
                if 'configMap' in volume:
                    item = volume['configMap']
                    data = maps[item['name']]
                    items = item.get('items', [{'key': key, 'path': key} for key in data])
                elif 'secret' in volume:
                    item = volume['secret']
                    data = secrets[item['secretName']]
                    items = item.get('items', [{'key': key, 'path': key} for key in data])
                else:
                    continue
                for item in items:
                    file = target / item['path']
                    file.parent.mkdir(parents=True, exist_ok=True)
                    file.write_text(data[item['key']])
                    file.chmod(0o444)
            storage['data'] = directory / 'home'
            storage['data'].mkdir(mode=0o755)
            (storage['config'] / 'fixture-sidecar.cjs').write_text((SCRIPTS / 'hermes_webapp_loopback.cjs').read_text())
            command('docker', 'run', '--rm', '--pull=never', '--network=none', '--user=0:0', '--cap-drop=ALL', '--cap-add=CHOWN',
                    '--mount', f'type=bind,src={storage["data"]},dst=/new-home', '--entrypoint=chown', image,
                    '1024:1024', '/new-home', stdout=subprocess.DEVNULL)

            def environment(container, filename):
                env = {'HOME': '/home/vibe', 'TMPDIR': '/tmp', 'PYTHONDONTWRITEBYTECODE': '1'}
                for item in container.get('env', []):
                    if 'value' in item:
                        env[item['name']] = item['value']
                    elif 'secretKeyRef' in item.get('valueFrom', {}):
                        ref = item['valueFrom']['secretKeyRef']
                        env[item['name']] = secrets[ref['name']][ref['key']]
                    elif 'fieldRef' in item.get('valueFrom', {}):
                        env[item['name']] = 'browser-' + name
                    else:
                        raise ValueError('unsupported fixture environment input')
                file = directory / filename
                if 'CETIRUN_INIT_IMAGE' in env:
                    env['CETIRUN_INIT_IMAGE'] = 'local.cetirun.invalid/hermes-gateway@' + image_identity
                file.write_text(''.join(key + '=' + str(value) + '\n' for key, value in env.items()))
                file.chmod(0o600)
                return file, env

            def mounts(container):
                result = []
                entries = container.get('volumeMounts', [])
                for parent in entries:
                    for child in entries:
                        relative = child['mountPath'].removeprefix(parent['mountPath'] + '/')
                        if relative != child['mountPath'] and parent['name'] != 'tmp':
                            (storage[parent['name']] / relative).mkdir(parents=True, exist_ok=True)
                for mount in entries:
                    if mount['name'] == 'tmp':
                        result += ['--tmpfs', mount['mountPath'] + ':rw,mode=1777']
                    else:
                        src = storage[mount['name']]
                        if 'subPath' in mount:
                            src /= mount['subPath']
                        result += ['--mount', f'type=bind,src={src},dst={mount["mountPath"]}' + (',readonly' if mount.get('readOnly') else '')]
                return result

            init = next(container for container in pod['initContainers'] if container['name'] == 'init-home-data')
            envfile, _ = environment(init, 'init.env')
            with (directory / 'init.log').open('w') as log:
                command('docker', 'run', '--rm', '--pull=never', '--network=none', '--user=1024:1024', '--read-only',
                        '--cap-drop=ALL', '--security-opt=no-new-privileges', '--env-file', envfile, *mounts(init),
                        '--entrypoint', init['command'][0], image, *init['command'][1:], stdout=log, stderr=log)
            main = pod['containers'][0]
            envfile, env = environment(main, 'main.env')
            container = network + '-' + name
            containers.append(container)
            command('docker', 'run', '-d', '--name', container, '--pull=never', '--network', network, '--user=1024:1024',
                    '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--env-file', envfile, *mounts(main),
                    '--mount', f'type=bind,src={REPO}/app/cetirun-chart/tests/fixtures/cetirun-qualification,dst=/qualification-source,readonly',
                    image, stdout=subprocess.DEVNULL)
            backend = capture('docker', 'inspect', container, '--format', '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
            command('docker', 'exec', '-d', container, 'node', '/etc/cetirun/fixture-sidecar.cjs')
            backends[prefix] = {'hostname': backend, 'port': 19120}
            instances.append({'prefix': prefix, 'password': env['API_SERVER_KEY']})
            (out / 'backends.json').write_text(json.dumps(backends))
            ready = False
            for _ in range(90):
                result = subprocess.run(['docker', 'exec', container, '/usr/local/bin/hermes-python', '/etc/cetirun/hermes-runtime.py', 'readiness'],
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                if result.returncode == 0:
                    ready = True
                    break
                time.sleep(1)
            if not ready:
                with (directory / 'container.log').open('w') as log:
                    subprocess.run(['docker', 'logs', container], stdout=log, stderr=log)
                raise RuntimeError('candidate ' + name + ' did not become Ready; inspect private fixture log')
            sha = capture('docker', 'exec', container, 'sh', '-c',
                'set -eu; cp -R /qualification-source /home/vibe/qualification-plugin; '
                'git -C /home/vibe/qualification-plugin init -q -b main; '
                'git -C /home/vibe/qualification-plugin add .; '
                'git -C /home/vibe/qualification-plugin -c user.name="Synthetic Tester" -c user.email=synthetic@example.com -c commit.gpgsign=false commit -qm "Synthetic plugin fixture"; '
                'git -C /home/vibe/qualification-plugin rev-parse HEAD')
            instances[-1]['pluginSHA'] = sha
        data = {'origin': origin, 'instances': instances, 'outputDir': str(out / 'browser'), 'artifact': True, 'files': True, 'sse': True,
                'executablePath': args.chromium}
        command('node', REPO / 'scripts/tests/hermes_webapp_browser.cjs', input=json.dumps(data), text=True,
                env={**os.environ, 'PLAYWRIGHT_MODULE': args.playwright_module})
        completions = [request for request in model_requests if request['path'] == '/v1/chat/completions']
        assert completions and all(request['model'] == 'synthetic-model' and request['status'] == 200 for request in completions)
        assert any(request['stream'] and request['roles'] == ['system', 'user'] for request in completions)
        assert all(request['status'] == 404 for request in model_requests if request not in completions)
        (out / 'model-requests.json').write_text(json.dumps(model_requests))
    finally:
        model.shutdown()
        model.server_close()
        model_thread.join()
        for container in containers:
            subprocess.run(['docker', 'rm', '-f', container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        proxy.terminate()
        proxy.wait(timeout=10)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--image', required=True)
    parser.add_argument('--helm', default='helm')
    parser.add_argument('--chromium', required=True)
    parser.add_argument('--playwright-module', default='playwright')
    parser.add_argument('--output-parent', type=Path)
    args = parser.parse_args()
    out = Path(tempfile.mkdtemp(prefix='hermes-webapp-browser-', dir=args.output_parent))
    out.chmod(0o755)
    print('Fixture evidence: ' + str(out), flush=True)
    fixture(args, out)
