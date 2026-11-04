# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

"""Exercise the pinned gateway over real loopback HTTP, WebSockets, PTYs and its durable stores.

Only the Discord-class wire and model replies are synthetic. The daemon uses upstream
start_gateway, GatewayRunner, session authority, FIFO and delivery code. Upstream's
pinned fake-platform fixture supplies the wire; it does not replace the gateway lifecycle.
"""

import asyncio
import base64
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import sys
import threading
from urllib.parse import urlencode

from native_network import install

install()

import httpx
from websockets.asyncio.client import connect

from tests.e2e.core.delivery._fake_platform import GatewayProcess
from hermes_cli.gateway_client import private_control_query


class Model(BaseHTTPRequestHandler):

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        self.server.requests.append(body)
        text = next((m.get('content', '') for m in reversed(body.get('messages', [])) if m['role'] == 'user'), '')
        markers = ('FROM_DISCORD', 'CONTINUE_FROM_WEBAPP', 'FOLLOWUP_FROM_WEBAPP', 'SYNTHETIC_CRON_AGENT')
        marker = next((value for value in markers if value in str(text)), 'OTHER')
        message = {'role': 'assistant', 'content': 'SYNTHETIC_ACK ' + marker}
        payload = json.dumps({'id': 'synthetic', 'object': 'chat.completion', 'created': 1, 'model': 'synthetic-model', 'choices': [{'index': 0, 'message': message, 'finish_reason': 'stop'}], 'usage': {'prompt_tokens': 10, 'completion_tokens': 5, 'total_tokens': 15}}).encode()
        kind = 'application/json'
        if body.get('stream'):
            payload = ('data: ' + json.dumps({'id': 'synthetic', 'object': 'chat.completion.chunk', 'created': 1, 'model': 'synthetic-model', 'choices': [{'index': 0, 'delta': message, 'finish_reason': None}]}) + '\n\ndata: ' + json.dumps({'id': 'synthetic', 'object': 'chat.completion.chunk', 'choices': [{'index': 0, 'delta': {}, 'finish_reason': 'stop'}]}) + '\n\ndata: [DONE]\n\n').encode()
            kind = 'text/event-stream'
        self.send_response(200)
        self.send_header('Content-Type', kind)
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        try:
            self.wfile.write(payload)
        except (BrokenPipeError, ConnectionResetError):
            pass


async def daemon(spool):
    from tests.e2e.core.delivery._fake_platform import ChildWorld, _adapter_class, _append_jsonl
    from gateway.config import GatewayConfig, HomeChannel, Platform, PlatformConfig, StreamingConfig
    from gateway.platform_registry import PlatformEntry, platform_registry
    from gateway.run_bootstrap import start_gateway
    base = _adapter_class()
    world = ChildWorld(spool)

    class Adapter(base):

        async def connect(self, **kwargs):
            result = await super().connect(**kwargs)

            async def handle(reader, writer):
                while (line := (await reader.readline())):
                    req = json.loads(line)
                    try:
                        if req['cmd'] == 'inject':
                            await self.inject(req['text'], req['message_id'], chat_id=req['chat_id'])
                            reply = {'ok': True}
                        elif req['cmd'] == 'seed_history':
                            from gateway.run import _gateway_runner_ref
                            db = _gateway_runner_ref().session_authority.db
                            for kind in ('webui', 'cron', 'subagent'):
                                db.create_session('legacy-' + kind, kind, model='synthetic-model')
                                db.append_message('legacy-' + kind, 'user', 'SYNTHETIC_OLD_HISTORY')
                            reply = {'ok': True}
                        elif req['cmd'] == 'busy':
                            reply = {'ok': True, 'busy': list(self._active_sessions), 'running': list(self._session_tasks)}
                        else:
                            raise ValueError('unsupported fixture verb')
                    except Exception as exc:
                        reply = {'ok': False, 'error': str(exc)}
                    writer.write((json.dumps(reply) + '\n').encode())
                    await writer.drain()
            self.fixture_server = await asyncio.start_server(handle, '127.0.0.1', 0)
            _append_jsonl(spool / 'ready.jsonl', {'pid': os.getpid(), 'port': self.fixture_server.sockets[0].getsockname()[1]})
            return result
    platform_registry.register(PlatformEntry(name='fk_dc', label='Synthetic Discord', check_fn=lambda: True, source='plugin', adapter_factory=lambda pc: Adapter(pc, name='fk_dc', profile='discord', world=world), allow_all_env='FK_DC_ALLOW_ALL_USERS'))
    os.environ['FK_DC_ALLOW_ALL_USERS'] = 'true'
    platform = Platform('fk_dc')
    cfg = GatewayConfig(platforms={platform: PlatformConfig(enabled=True, token='synthetic-token', home_channel=HomeChannel(platform=platform, chat_id='home', name='home'))}, streaming=StreamingConfig(enabled=True, transport='edit', edit_interval=0.05, buffer_threshold=24, cursor=''), loop_watchdog=False, write_sessions_json=False, multiplex_profiles=False)
    assert await start_gateway(cfg)


if len(sys.argv) > 1 and sys.argv[1] == '--daemon':
    asyncio.run(daemon(Path(sys.argv[2])))
    raise SystemExit(0)


class Process(GatewayProcess):

    def env(self):
        env = super().env()
        env.update(HERMES_RUNTIME_DIR=str(source / 'tools'), HERMES_WEBAPP_DIST=str(source / 'apps/desktop/dist-webapp'), CETIRUN_GATEWAY_API_KEY='synthetic-model-key', PYTHONDONTWRITEBYTECODE='1')
        return env

    def start(self):
        from tests.e2e.core.delivery._fake_platform import read_jsonl, wait_until
        self.boots += 1
        before = len(read_jsonl(self.spool / 'ready.jsonl'))
        with self.log.open('wb') as log:
            self.proc = subprocess.Popen([sys.executable, __file__, '--daemon', str(self.spool)], cwd=source, env=self.env(), stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        self.pids.append(self.proc.pid)
        ready = wait_until(lambda: next((r for r in read_jsonl(self.spool / 'ready.jsonl')[before:] if r['pid'] == self.proc.pid), None), 'fixture wire', timeout=90, proc=self.proc, log=self.log)
        self.sock = socket.create_connection(('127.0.0.1', ready['port']), timeout=90)
        self._rfile = self.sock.makefile('rb')

        def ready_state():
            try:
                request = json.dumps({'protocol': 1, 'id': 1, 'verb': 'identify', 'params': {}}).encode() + b'\n'
                response = json.loads(private_control_query(self.hermes_home, request, timeout=1))
                return response.get('result', {}).get('state') == 'ready'
            except (OSError, ValueError):
                return False
        wait_until(ready_state, 'actual gateway ready', timeout=90, proc=self.proc, log=self.log)
        return self


async def rpc(ws, method, **params):
    await ws.send(json.dumps({'jsonrpc': '2.0', 'id': method, 'method': method, 'params': params}))
    async with asyncio.timeout(45):
        while True:
            result = json.loads(await ws.recv())
            if result.get('id') == method:
                return result


async def probe(gw, port):
    origin = f'http://127.0.0.1:{port}'
    token = (gw.hermes_home / 'webapp/session-token').read_text().strip()
    async with httpx.AsyncClient(base_url=origin, headers={'X-Hermes-Session-Token': token}, timeout=30) as client:

        async def ticket(kind):
            result = await client.post('/api/webapp/socket-ticket', params={'socket': kind})
            result.raise_for_status()
            return result.json()['ticket']
        async with connect(origin.replace('http:', 'ws:') + '/api/ws?native_dial=browser', origin=origin, subprotocols=['hermes-gateway-v1', 'hermes-gateway-ticket.' + await ticket('gateway')]) as ws:
            listing = await rpc(ws, 'session.list')
            rows = listing['result']['sessions']
            assert len(rows) == 1 and rows[0]['source'] == 'fk_dc'
            sid = rows[0]['session_id']
            resumed = await rpc(ws, 'session.resume', session_id=sid)
            assert resumed['result']['session_id'] == sid and resumed['result']['stored_session_id'] == sid
            first = await rpc(ws, 'prompt.submit', session_id=sid, input_id='webapp-continued', text='CONTINUE_FROM_WEBAPP')
            second = await rpc(ws, 'prompt.submit', session_id=sid, input_id='webapp-followup', text='FOLLOWUP_FROM_WEBAPP')
            assert first['result']['ref']['session_id'] == second['result']['ref']['session_id'] == sid
            assert first['result']['status'] == second['result']['status'] == 'queued'
            assert second['result']['sequence'] == first['result']['sequence'] + 1
            await ws.close()
            for _ in range(300):
                await asyncio.sleep(0.1)
                messages = [m for m in gw.platform_view().messages.values() if not m.deleted]
                if any(('FOLLOWUP_FROM_WEBAPP' in m.text for m in messages)):
                    break
            assert any(('CONTINUE_FROM_WEBAPP' in m.text and m.chat_id == 'synthetic-chat' for m in messages))
            assert any(('FOLLOWUP_FROM_WEBAPP' in m.text and m.chat_id == 'synthetic-chat' for m in messages))
            assert gw.proc.poll() is None
        gw.rpc('seed_history')
        async with connect(origin.replace('http:', 'ws:') + '/api/ws?native_dial=browser', origin=origin, subprotocols=['hermes-gateway-v1', 'hermes-gateway-ticket.' + await ticket('gateway')]) as ws:
            listing = await rpc(ws, 'session.list')
            assert len(listing['result']['sessions']) == 1
            for kind in ('webui', 'cron', 'subagent'):
                old = await rpc(ws, 'session.resume', session_id='legacy-' + kind)
                assert 'SYNTHETIC_OLD_HISTORY' in json.dumps(old['result']['messages'])
                details = await client.get('/api/sessions/legacy-' + kind + '/messages')
                details.raise_for_status()
                assert 'SYNTHETIC_OLD_HISTORY' in details.text
                attempt = await rpc(ws, 'prompt.submit', session_id='legacy-' + kind, input_id='legacy-' + kind + '-attempt', text='UNBOUND_HISTORY_MUST_NOT_EXECUTE')
                assert attempt['error']['message'] == 'session_read_only'
        work = gw.home / 'workspace'
        work.mkdir()
        file = work / 'report.txt'
        file.write_text('original\n')
        env = {'PATH': os.environ['PATH'], 'HOME': str(gw.home), 'GIT_CONFIG_NOSYSTEM': '1'}
        for args in (['init', '-b', 'main'], ['add', 'report.txt'], ['-c', 'user.name=Synthetic Tester', '-c', 'user.email=synthetic@example.com', 'commit', '-m', 'Synthetic fixture']):
            subprocess.run(['git', '-C', str(work), *args], env=env, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        data = b'updated through Webapp\n'
        uploaded = await client.post('/api/files/upload', json={'path': str(file), 'data_url': 'data:text/plain;base64,' + base64.b64encode(data).decode()})
        uploaded.raise_for_status()
        listed = await client.get('/api/files', params={'path': str(work)})
        listed.raise_for_status()
        assert any((e['name'] == 'report.txt' for e in listed.json()['entries']))
        minted = await client.post('/api/files/ticket', json={'route': 'download', 'path': str(file)})
        minted.raise_for_status()
        async with httpx.AsyncClient(base_url=origin) as browser:
            downloaded = await browser.get('/api/fs/download', params={'path': str(file), 'ticket': minted.json()['ticket']})
            downloaded.raise_for_status()
            assert downloaded.content == data
            preview = await browser.get('/assets/webapp-preview.html')
            assert preview.status_code == 200 and 'hermes-preview-document' in preview.text
        status = await client.get('/api/git/status', params={'path': str(work)})
        status.raise_for_status()
        diff = await client.get('/api/git/file-diff', params={'path': str(work), 'file': 'report.txt'})
        diff.raise_for_status()
        assert '+updated through Webapp' in diff.json()['diff']

        async def terminal(extra=''):
            return await connect(origin.replace('http:', 'ws:') + '/api/host-terminal?' + urlencode({'ticket': await ticket('host-terminal')}) + extra, origin=origin)

        async def meta(ws):
            message = await ws.recv()
            assert isinstance(message, str) and message.startswith('\x00HERMES_TERMINAL_META:')
            return json.loads(message.split(':', 1)[1])

        async def output(ws, needle):
            seen = b''
            async with asyncio.timeout(20):
                while needle not in seen:
                    frame = await ws.recv()
                    seen += frame if isinstance(frame, bytes) else frame.encode()
            return seen
        term = await terminal('&cwd=' + str(work))
        metadata = await meta(term)
        term_id = metadata['terminalId']
        await term.send(b"stty -echo; continuity=retained; printf 'REA%s=%s\\n' DY $$\n")
        body = await output(term, b'READY=')
        pid = int(re.search(b'READY=(\\d+)', body)[1])
        await term.close()
        term = await terminal('&attach=' + term_id)
        metadata = await meta(term)
        assert metadata['reconnected'] and metadata['terminalId'] == term_id
        await term.send(b'printf \'STA%s=%s:%s\\n\' TE "$continuity" $$\n')
        body = await output(term, b'STATE=retained:')
        assert b'STATE=retained:' + str(pid).encode() in body
        await term.close()
        term = await terminal('&attach=' + term_id + '&action=close')
        assert (await meta(term))['closed']
        await term.close()
        scripts = gw.hermes_home / 'scripts'
        scripts.mkdir(exist_ok=True)
        (scripts / 'qualification.sh').write_text('#!/bin/sh\nprintf "CRON_QUALIFIED\\n"\n')
        job = await client.post('/api/cron/jobs', json={'name': 'Synthetic cron', 'schedule': 'every 1h', 'script': 'qualification.sh', 'no_agent': True, 'deliver': 'local', 'workdir': str(work)})
        job.raise_for_status()
        job_id = job.json()['id']
        assert job.json()['profile'] == 'default'
        run = await client.post('/api/cron/jobs/' + job_id + '/trigger')
        run.raise_for_status()
        history = await client.get('/api/cron/jobs/' + job_id + '/runs')
        history.raise_for_status()
        assert 'CRON_QUALIFIED' in history.text and 'COMPLETED' in history.text
        paused = await client.post('/api/cron/jobs/' + job_id + '/pause')
        paused.raise_for_status()
        assert paused.json()['state'] == 'paused'
        agent_job = await client.post('/api/cron/jobs', json={
            'name': 'Synthetic agent cron', 'schedule': 'every 1h', 'prompt': 'SYNTHETIC_CRON_AGENT',
            'enabled_toolsets': [], 'model': 'synthetic-model', 'provider': 'cetirun-gateway',
            'deliver': 'local', 'workdir': str(work),
        })
        agent_job.raise_for_status()
        agent_job_id = agent_job.json()['id']
        assert agent_job.json()['profile'] == 'default'
        agent_run = await client.post('/api/cron/jobs/' + agent_job_id + '/trigger')
        agent_run.raise_for_status()
        agent_history = await client.get('/api/cron/jobs/' + agent_job_id + '/runs')
        agent_history.raise_for_status()
        agent_record = agent_history.json()['runs'][0]
        assert agent_record['end_reason'] == 'cron_complete' and agent_record['profile_name'] == 'default'
        agent_messages = await client.get('/api/sessions/' + agent_record['id'] + '/messages')
        agent_messages.raise_for_status()
        assert 'SYNTHETIC_ACK SYNTHETIC_CRON_AGENT' in agent_messages.text
        slow = scripts / 'qualification-slow.sh'
        pidfile = work / 'cron.pid'
        slow.write_text('#!/bin/sh\necho $$ > ' + str(pidfile) + '\nsleep 60\n')
        slowjob = await client.post('/api/cron/jobs', json={'name': 'Synthetic slow cron', 'schedule': 'every 1h', 'script': slow.name, 'no_agent': True, 'deliver': 'local', 'workdir': str(work)})
        slowjob.raise_for_status()
        slowid = slowjob.json()['id']
        running = asyncio.create_task(client.post('/api/cron/jobs/' + slowid + '/trigger'))
        for _ in range(200):
            if pidfile.exists():
                break
            await asyncio.sleep(0.05)
        assert pidfile.exists()
        canceled = await client.post('/api/cron/jobs/' + slowid + '/cancel')
        canceled.raise_for_status()
        assert canceled.json()['cancelled']
        (await asyncio.wait_for(running, 20)).raise_for_status()
        stopped = await client.get('/api/cron/jobs/' + slowid + '/runs')
        stopped.raise_for_status()
        assert 'CANCELLED' in stopped.text
        try:
            os.kill(int(pidfile.read_text()), 0)
        except ProcessLookupError:
            pass
        else:
            raise AssertionError('cancelled cron process survived')
        term = await terminal('&cwd=' + str(work))
        metadata = await meta(term)
        stale_terminal = metadata['terminalId']
        await term.send(b"stty -echo; printf 'RESTART_P%s=%s\\n' ID $$\n")
        body = await output(term, b'RESTART_PID=')
        old_pid = int(re.search(b'RESTART_PID=(\\d+)', body)[1])
        await term.close()
        old_gateway = gw.proc.pid
        gw.stop()
        gw.start()
        assert gw.proc.pid != old_gateway
        assert (gw.hermes_home / 'webapp/session-token').read_text().strip() == token
        try:
            os.kill(old_pid, 0)
        except ProcessLookupError:
            pass
        else:
            raise AssertionError('host shell survived gateway restart')
        stale = await terminal('&attach=' + stale_terminal)
        from websockets.exceptions import ConnectionClosed
        try:
            await stale.recv()
        except ConnectionClosed as exc:
            assert exc.code == 4410
        else:
            raise AssertionError('old host terminal was reattached after restart')
        async with connect(origin.replace('http:', 'ws:') + '/api/ws?native_dial=browser', origin=origin, subprotocols=['hermes-gateway-v1', 'hermes-gateway-ticket.' + await ticket('gateway')]) as ws:
            restored = await rpc(ws, 'session.resume', session_id=sid)
            assert 'result' in restored and 'FOLLOWUP_FROM_WEBAPP' in json.dumps(restored['result']['messages'])
source = Path(os.environ['HERMES_QUALIFICATION_SOURCE'])
root = Path(os.environ['HERMES_QUALIFICATION_WORKDIR']) / 'gateway'
root.mkdir()
server = ThreadingHTTPServer(('127.0.0.1', 0), Model)
server.requests = []
threading.Thread(target=server.serve_forever, daemon=True).start()
port_socket = socket.socket()
port_socket.bind(('127.0.0.1', 0))
port = port_socket.getsockname()[1]
port_socket.close()
gw = Process(root, platforms={'fk_dc': 'discord'}, llm_base_url=f'http://127.0.0.1:{server.server_port}/v1', extra_config=f'webapp:\n  enabled: true\n  host: 127.0.0.1\n  port: {port}\nauxiliary:\n  title_generation:\n    enabled: false\nplatform_toolsets:\n  fk_dc: []\n')
from ruamel.yaml import YAML
yaml = YAML()
cfg = yaml.load((gw.hermes_home / 'config.yaml').read_text())
cfg['model'] = {'provider': 'cetirun-gateway', 'default': 'synthetic-model'}
cfg['providers'] = {'cetirun-gateway': {'name': 'CetiRun AI Gateway', 'api': f'http://127.0.0.1:{server.server_port}/v1', 'key_env': 'CETIRUN_GATEWAY_API_KEY', 'transport': 'chat_completions', 'default_model': 'synthetic-model', 'discover_models': False, 'models': {'synthetic-model': {}}}}
(gw.hermes_home / 'config.yaml').write_text(json.dumps(cfg))
(gw.hermes_home / 'models_dev_cache.json').write_text('{"hermes-test-offline":{"id":"hermes-test-offline","env":[],"models":{}}}')
try:
    gw.start()
    gw.inject('fk_dc', 'FROM_DISCORD', 'synthetic-message', 'synthetic-chat')
    gw.wait_idle(['synthetic-chat'], 'channel initial task', timeout=60)
    asyncio.run(probe(gw, port))
    assert all(('UNBOUND_HISTORY_MUST_NOT_EXECUTE' not in json.dumps(request) for request in server.requests))
    assert any(('FROM_DISCORD' in json.dumps(request) and 'FOLLOWUP_FROM_WEBAPP' in json.dumps(request) for request in server.requests))
    print(json.dumps({'channel_session_fifo_delivery': True, 'unbound_history_read_only': True, 'files_upload_download': True, 'git_status_diff': True, 'preview_asset': True, 'cron_run_cancel_profile': True, 'terminal_reconnect_close_restart': True, 'history_and_browser_credential_restart': True}))
finally:
    gw.stop()
    server.shutdown()
