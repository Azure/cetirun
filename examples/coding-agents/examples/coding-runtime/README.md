# Coding Runtime 示例

这是同一 Custom Chat 接口下的三个 Runtime 适配示例，不是 cetirun 新的内置
Harness。**默认完全离线**；mock 的审查结果是固定合成数据，不是模型生成，
不执行工具、CLI 或 subagent。

## 运行与检查

macOS/Linux、Python 3.11+，只使用标准库：

```bash
make test check
python3 runtime.py run --runtime copilot
python3 runtime.py run --runtime claude
python3 runtime.py run --runtime codex
python3 runtime.py serve --runtime claude --port 8080
```

同一时间一个 server 对应一个 runtime。运行多个实例时使用不同端口。
默认监听 `127.0.0.1`；这是开发服务，没有应用鉴权，不要公开发布。

```bash
curl --fail http://127.0.0.1:8080/healthz
curl --fail http://127.0.0.1:8080/v1/models
curl --fail -N http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"mock/claude","messages":[{"role":"user","content":"Review pricing.py."}],"stream":true}'
```

| 接口 / 行为 | 实现 |
| --- | --- |
| `GET /healthz` | 只报告进程就绪，不调用模型 |
| `GET /v1/models` | 只返回 `mock/<runtime>` 或 `native-default/<runtime>`，后者是本地适配器别名，不是 provider 模型 ID |
| `POST /v1/chat/completions` | 文本 messages、JSON completion 或 SSE completion chunks |
| 并发 | 每个实例只执行一个请求，其他请求返回 409；不排队、不重试 |
| 限制 | 64 KiB 请求、最多 32 条消息、256 KiB 原生输出、120 秒原生任务 deadline |
| 原生错误 | 固定错误码，不把 stderr/provider 错误文本返回客户端 |
| 原生取消 | 客户端断开时检查取消，终止任务进程组；不能撤销已发生的 provider 计费或外部副作用 |
| 会话 | 每次请求重建上下文；不恢复 CLI 会话，不实现跨 Runtime 的 session 迁移 |

SSE **先缓冲原生任务的最终结果，再发送 content、finish、`[DONE]`**。
它不是实时 tool/token stream；没有运行中 keepalive。开始 SSE 前的原生错误
返回 HTTP 错误，不发送成功标记。实际 Portal/反向代理可能因静默阶段超时，
因此这个简化版尚不能作为生产 Portal Chat 流式适配器。

服务仅实现文本子集，不支持图片、tool messages、外部 tool calling 或多个
completion。`tools`、`tool_choice`、`n` 会被拒绝；temperature/token 等其他
字段不映射到原生 CLI。原生 Runtime 的默认模型和内部 token/tool 预算仍由
Runtime 配置控制；本例只约束外层时间、输出和并发。

## 三家原生配置

| Runtime | 配置 | 启动方式 |
| --- | --- | --- |
| GitHub Copilot CLI | `workspace/.github/agents/cetirun-reviewer.agent.md` | `--agent cetirun-reviewer -p <prompt>`，自定义角色作为当前会话 Agent |
| Claude Code | `workspace/.claude/agents/cetirun-reviewer.md` | `--agent cetirun-reviewer -p`，选择自定义 Agent 为当前会话 |
| Codex CLI | `workspace/.codex/agents/cetirun-reviewer.toml` | `codex exec`，提示主 Agent 委派给该角色并等待结果 |

不指定模型，继承各 Runtime 默认。Codex 没有在本例中使用虚构的
`--agent` 参数，也不把 `AGENTS.md` 误当角色注册文件。Codex 的真实委派是否发生
必须通过原生事件/线程记录验收；HTTP completion 不能证明发生了 subagent。
本例不会解析完整 thread 拓扑或把成功回答强行当作委派成功。

## 可选：本地真实 CLI

先自行安装并验证获准的 CLI 版本，在其原生流程完成登录。不自动安装、
读取 token 文件、共享凭据或使用 `--yolo`/跳过全部权限。
本机只确认了 Copilot CLI 的 help；Claude/Codex CLI 及真实模型未运行验证。
参数与角色格式以官方文档为基准：
[Copilot CLI](https://docs.github.com/en/copilot/how-tos/copilot-cli/customize-copilot/create-custom-agents-for-cli)、
[Claude Code](https://code.claude.com/docs/en/sub-agents) 和
[Codex](https://developers.openai.com/codex/subagents)。

先查看命令计划，不启动 CLI、不调用模型：

```bash
python3 runtime.py plan --runtime copilot
python3 runtime.py plan --runtime claude
python3 runtime.py plan --runtime codex
```

明确同意由已安装 CLI 处理合成 fixture 后，才运行：

```bash
python3 runtime.py run --runtime copilot --native --ack-data-export
python3 runtime.py run --runtime claude --native --ack-data-export
python3 runtime.py run --runtime codex --native --ack-data-export
```

本地原生 HTTP 可用 `serve --native --ack-data-export`，客户端的 model 改为
`native-default/<runtime>`。**不要把私有代码、机密或敏感消息传入此演示服务。**
CLI 可加载原生用户配置/上下文，并可能发送数据给其服务商；工具限制和提示
不是文件系统、网络或多租户隔离。Copilot prompt 通过命令参数传递，可能出现在
本机进程列表；Claude/Codex prompt 经非阻塞 stdin 传递。不要在 prompt 放凭据。

只读角色审查 `workspace/pricing.py`，不会修复文件。fixture 故意把 20 当作
0.20，便于检查真实 CLI 能否识别相同问题。HTTP 完成只表示外层任务结束，
不证明审查正确、角色发现成功或内部委派完成。

设置 `OPENAI_BASE_URL` 或存在任意 `CETIRUN_*` 环境变量时，本例拒绝原生模式，
返回 `platform_native_not_qualified`。不要移除平台变量规避限制：此处的本地 CLI
不是已经通过 cetirun Gateway 资格验证的产品实现。

## 容器与平台参考

`Dockerfile` 只打包 **copilot 标签的离线 mock**，不安装任何真实 Coding CLI。
运行用户为 `1024:1024` / `vibe`，应用在 `/opt`，可写 HOME 与应用分离。
管理员应提供已批准、满足 cetirun 初始化工具契约且有 Python 3.11+ 的 base
镜像；本例没有猜测可用的官方镜像版本或 digest。

```bash
docker build --build-arg BASE_IMAGE='<approved-cetirun-compatible-base@sha256:digest>' \
  -t coding-runtime-example:local .
docker run --rm --read-only --tmpfs /tmp --cap-drop ALL \
  -p 127.0.0.1:8080:8080 coding-runtime-example:local
```

Dockerfile 校验常用初始化工具和 UID/GID，但不替代完整 HOME 初始化/CSI 验收。
`CETIRUN_BIND_HOST` 优先于 `--host`，保留平台 mTLS 的 loopback 要求。

`cetirun-agent.yaml.template` 是字段参考，**不可直接 apply**。填入已存在的
Team、Sandbox、用户、兼容 chart 和获准的生产镜像 digest；生产镜像应是完成
Gateway 接入的独立实现，不应使用此 mock 冒充模型 Runtime。Custom CRD 不接受
任意命令/env 覆盖；分别构建单 Runtime 镜像，或由镜像自有入口选择 Runtime。

本次未运行 Docker build：本机 Docker daemon 不可用，且未提供已批准 base。
未运行 Helm render、Portal、AKS、Gateway、模型、真实 OAuth/MCP 或 PVC 验收。
因为 upstream 源码未变，未运行其 Go/前端/root make 全套检查；本例的
`make test check` 只覆盖独立交付目录。

## 本次离线验证结果

2026-11-08：`make test check` 通过，22 个测试覆盖 HTTP、SSE、输入拒绝、
并发拒绝、原生输出归一化、超时、取消、输出限额和平台 native guard。
原生子进程测试使用 Python 合成 CLI，不使用真实服务商。
三个 Runtime 标签另经实际 server 进程验证 `/healthz`、`/v1/models`、
JSON completion、SSE completion 与 CLI plan；SIGTERM 后全部正常退出。
这些结果不证明真实 CLI 的角色发现、委派、provider 或生产平台兼容性。
