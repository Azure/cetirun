# 真实 Coding Runtime 应用

此目录没有 mock 分支。每个镜像只安装一个获校验的原生 Runtime：

| 镜像 | 原生 CLI | 运行用户 |
| --- | --- | --- |
| `cetirun-copilot:local` | GitHub Copilot CLI 1.0.107 | `1024:1024` |
| `cetirun-claude:local` | Claude Code 2.1.319 | `1024:1024` |
| `cetirun-codex:local` | Codex CLI 0.167.0 | `1024:1024` |

原生官方二进制来源和 SHA-256 见 `artifacts.json`。基础镜像固定为已查到的
CetiRun base digest，保留 HOME 初始化工具契约。安装过程不运行远程 installer，
不把凭据放入构建参数、镜像层、版本文件或日志。
npm mirror 本次对三个包均返回 404，因此使用校验过的官方原生二进制，
没有偷偷切换到公共 npm 源。

## 功能与接入

`server.py` 提供 `/healthz`、`/v1/models`、`/v1/chat/completions` 与本地网页。
Chat 运行原生工具，可以创建、修改代码和执行测试；不再限制到合成 `pricing.py`。
Workspace 保留在 `$HOME/workspace`，三家配置按原生路径首次初始化；
已有用户文件不被覆盖。HTTP 每次从 messages 重建任务上下文，不自动恢复或
跨 Runtime 转移会话。CLI 自身的会话文件保留在各自的 HOME 目录；
适配器管理的 Copilot 会话位于 `$HOME/.cetirun/coding-runtime/copilot-sessions`，
不迁移或删除用户原有的 `.copilot` 会话。

模型访问必须使用平台注入的 `OPENAI_BASE_URL` 和
`OPENAI_API_KEY` **每 Agent Virtual Key**。`/v1/models` 实时读取该 key
获准的 Gateway catalog；模型不可用、未授权或 Gateway 失败不返回假模型，
不转为个人账号或直接 provider API。

| Runtime | Gateway 映射 |
| --- | --- |
| Copilot | 通过环境变量配置 Gateway，OpenAI Responses wire，显式选择 Gateway 原始授权模型 ID |
| Claude Code | Anthropic Messages 协议，去除 Gateway base 的末尾 `/v1` 避免重复路径，模型/subagent alias 保持同一授权模型 |
| Codex | 显式 `cetirun` provider，Responses wire，`env_key` 引用 Virtual Key，不依赖个人 OpenAI 登录 |

HTTP 和 `coding-tui` 共用同一启动准备逻辑。Copilot 使用独立、临时的
`COPILOT_HOME`、`settings.json` 和 `COPILOT_PROVIDERS_CONFIG`，不会读取旧 provider
作为模型路由，也不删除或覆盖用户原配置。provider JSON 为空 registry，不包含密钥；
Gateway Virtual Key 仅通过子进程环境传入，不写入适配器配置文件或 argv。
临时配置文件权限为 `0600`，父目录为 `0700`，正常退出和受控取消后清理。
CLI 偏好不从用户原配置继承，持久会话与临时配置分开保存。

平台只读规范视图为 `$HOME/.agents`。适配器将其中的 `AGENTS.md` 明确加入
Copilot 的受控主角色、Claude 的追加 system prompt，以及 Codex 的
`developer_instructions`；不是仅复制一个 Codex 子 Agent TOML。Copilot/Claude 的
主角色为 `cetirun-platform-coder`，用户的 `AGENTS.md`、`CLAUDE.md` 和原生角色文件
不被覆盖。超出 CLI 参数安全上限的平台指令明确报错，不偷偷截短。
技能通过 Copilot 的受控技能目录、Claude 的临时原生 plugin，以及 Codex 原生的
`$HOME/.agents/skills` 发现机制接入；Claude plugin 技能带 `cetirun-platform:` 前缀。

仅当平台同时提供 `CETIRUN_MEMORY_ASSETS`、可执行 helper 和 `CETIRUN_MEMORY_ROOT`
时注册 `cetirun-memory` stdio MCP：`cetirun-memory mcp --stdio --owner-private`。
使用平台提供的 root，不另建私有 Memory store；支持平台的 `CETIRUN_MEMORY_BINARY`
覆盖。HTTP 支持 `X-CetiRun-Memory-Mode: off` / `no-capture`，并与继承的
`CETIRUN_MEMORY_NO_MEMORY=true` / `CETIRUN_MEMORY_NO_CAPTURE=true` 取更严格限制。
`off` 不启动 helper、不启用平台 Memory 技能；`no-capture` 将禁止记录设置传给 helper。
三家 CLI 的独立自动 Memory 均禁用，避免绕开平台设置。这不是启用 Connections MCP。
终端按启动时的环境设置生效；原生 CLI 的 `/memory` 不等同于平台控制，切换平台限制
须设置上述环境并重新启动 `coding-tui`。禁止记录不代表删除原生 CLI 会话历史。

服务是 **私有单租户工作负载**，没有自建应用鉴权，不能公开发布
LoadBalancer/Ingress。通过 CetiRun 的平台授权 Chat/终端使用；本地网页仅供
获准的私有访问/port-forward。当前网页不实现 CetiRun Web UI 前缀契约，因此
Agent 必须保持 `webUI: false`，不能直接改为 `webUI: true`。

持久 HOME 不是隔离措施；真实文件/网络隔离仍由获准的 CetiRun Sandbox/Policy
实现。工具权限允许编码和测试，不使用 `--yolo` 或
`--dangerously-bypass-approvals-and-sandbox`。不应让多个 Runtime 共享可写 HOME
或 checkout。原生工具可能执行外部操作，取消不会撤销这些操作或 provider 计费。
模型调用、编程正确性和隔离均不能由 `/healthz` 单独证明。

SSE 在运行中发送文本事件及 15 秒 keepalive。每帧连同 JSON escaping、元数据和
`data:` 封装编码后不超过 64 KiB，中文和 emoji 按完整字符分段；全部文本成功写出后
才发送成功 finish / `[DONE]`。HTTP 任务期限 600 秒、并发 1、消息数最多 100、
请求最多 1 MiB、每条用户输入最多 64 KiB（UTF-8），最终文本 1 MiB、原生事件总量
8 MiB。历史消息完整传入，超限明确拒绝；交互式终端没有 HTTP 任务期限。
不自动排队或 replay。失败的 stream 发送固定错误码，不发送成功 finish / `[DONE]`。
若连接已经断开，则无法再投递错误帧，客户端应将缺失成功完成标志视为失败。
Copilot root idle 与子 Agent idle 区分；被取消的 idle 不算成功。
Native stderr 不输出到 HTTP；内部 token/tool 预算仍需平台和 Runtime 配置。
只支持文本消息，不支持多模态、外部 tools 或多 completion。

## 构建与验证

```bash
make test check
make build
```

还可在对应的固定版本镜像中执行原生协议回归；从 `app/` 运行：

```bash
for runtime in copilot claude codex; do
  for mode in off no-capture; do
    docker run --rm --platform linux/amd64 --network none --read-only \
      --tmpfs /tmp:rw,exec,nosuid,nodev,size=768m \
      --mount type=bind,source="$PWD",target=/opt/cetirun-coding,readonly \
      -e PYTHONDONTWRITEBYTECODE=1 --entrypoint python3 \
      "cetirun-$runtime:local" /opt/cetirun-coding/tests/native_probe.py "$runtime" "$mode"
  done
done
```

该检查只访问容器内的合成 Gateway，并在推理前返回合成错误；检查实际模型请求中的
Gateway 路由、平台主指令、技能发现、Memory 关闭及 no-capture MCP 环境传递。
Memory helper 也是合成 fixture，不访问真实私有 store，不进行服务商收费调用。

直接下载遇到 504 时，可以先通过获准的官方 release 下载流程把三个公开
artifact 保存到构建缓存目录，然后：

```bash
docker buildx build --platform linux/amd64 --load \
  --build-context runtime-artifacts='<official-artifact-cache-directory>' \
  --build-arg CODING_RUNTIME=copilot -t cetirun-copilot:local .
```

依次替换 runtime 为 `claude`、`codex`。缓存必须包含 `artifacts.json` 中 URL
末尾的官方文件名，所有文件仍重新验证 SHA-256。缓存只用于安装 stage，
不复制进最终镜像。镜像只支持 Linux amd64，符合已检查的目标节点架构。

平台配置好后，在该 Agent 的获准终端：

```bash
python3 /opt/cetirun-coding/server.py doctor --model '<authorized-model-id>'
coding-tui --model '<authorized-model-id>'
```

`doctor` 只验证 catalog 可达与模型授权，不做推理。真实验收必须另外完成一次
编码任务、文件修改、测试执行、错误/取消、挂起/恢复与 HOME 保留。

## 当前实测结果与未完成事项

三个镜像均已本地构建，官方 CLI 版本、关键参数和非 root 用户已经检查。
24 个原生适配器测试通过，包含真实 Python 子进程的 stdin、文件创建、
stream/授权/失败/取消、旧配置保留、Memory 限制、Unicode SSE 分段、传输失败、
请求边界、完整历史和 HTTP/TUI 期限区分；这些测试不是服务商推理测试。
固定版本的三个真实 CLI 在实际构建镜像中另通过 6 组离线
Gateway/context/skill/Memory 协议回归。
**本轮修订已重建发布到私有 ACR，并通过 CetiRun 控制器逐一更新三个线上 Agent。**
已读回验证新镜像的实际运行 digest、Agent Ready、原 HOME PVC 身份与 PV 绑定、
独立终端 Ready、未登录终端 302 跳转及严格 admission。发布和验证记录保存在
被 Git 忽略的 `../.deployment/runtime-contract-release.json`；旧镜像仍保留回滚资格。
三个实际镜像另外启动检查了健康、网页、缺失 Gateway 拒绝启动、
不可达 Gateway 不返回假 catalog，以及正常 SIGTERM 退出。
所有临时验证容器均已删除；没有使用已暴露的 provider key 调用模型。

**三个 Runtime 镜像已发布并固定 digest；CetiRun 控制面和三个 managed Agent 均已 Ready。**
目标现在已有实际 Keycloak/OIDC、Istio、cert-manager 和正式公网 TLS；CetiRun
Operator、Portal、AI Gateway 和严格 admission 均为 2/2 Ready，Gateway 的
PostgreSQL/Valkey 均为 1/1 Ready。原生 Sandbox/Team/AgentPolicy 已通过真实控制器
评估，三个 Agent 的 PolicyApplied 和 Ready 均为 True；还需要完整 owner 登录和真实模型
验收。轮换后的 provider Secret 已确认存在且字段非空，未展示或解码 Key；
AKS kubelet 的专用 ACR `AcrPull` 授权及三个镜像在 AKS 上实际运行 CLI 已验证。
runc 的 CPU 限额、内存 cgroup 上限、只读根文件系统与 Azure Disk HOME
跨节点重新挂载通过；两台节点 TCP/UDP、多端口 deny/allow、重叠 NetworkPolicy
下显式 deny 优先及节点 egress 阻断均通过。只注册原生 baseline，不声称完整 adapter
qualification、节点 ingress 隔离或物理单写者隔离。临时测试资源已清理。
匹配私有源码的 Runtime Chart 已验证只读拉取；五个匹配配套镜像已发布、
核验源码版本并用于控制面，不用旧公开二进制代替新保护逻辑。
目标已按授权经 1.36.5 升级到 Kubernetes 1.37.1，
控制面与两个节点池成功，节点、现有部署副本及 API readiness 检查通过，
Kata 配置保留。Agent 启动与安全修订已另行核验，不能仅凭版本门槛判断就绪；
不能伪造输入、绕过资格检查或关闭平台安全检查。

专用 Broker 加密托管身份、联邦凭据、Key Vault RSA-3072 Key 与最小范围授权
已创建，RSA-OAEP-256 wrap/unwrap 往返验证通过。Broker、MCP、客户端 mTLS 与
Admission 证书已签发并写入专用 namespace 的 Secrets，CA、SAN、用途、证书/
私钥匹配及存储后的公开证书一致性已检查。CA 私钥不进入 Kubernetes，已备份
到专用 Key Vault。内部叶证书有效至 **2027-02-06**，须在到期前按批准流程轮换。
Gateway 管理认证及 Valkey 凭据已安全生成，不打印值或覆盖既有 Secret。

已按用户选择改为组件 Helm 和正式的 Keycloak HTTPS OIDC。无需 Entra Portal
应用、Blueprint 或 Microsoft 委派注册，也没有提权：关闭 Operator 的身份
创建/清理、Portal 身份/Tools 功能、Broker 和 Connections MCP。保留 Portal
的受保护入口、管理员控制的唯一 alias 与登录组，不使用开发用 `local-oidc`。
原生 CLI 的 GitHub/模型授权仍分别处理，不用 Keycloak 登录代替模型授权。

Keycloak 26.8.2、持久 PostgreSQL、OIDC Proxy/Valkey 会话存储和专用身份网关
已实际就绪。公网/上游 TLS 均保留证书验证；受控声明、签名密钥、登录组、
精确 HTTPS 回调、PKCE 和标准登录会话刷新 scope 已实测；公网到真实登录表单
的跳转通过。身份网关独立于 Portal ownership 过滤器，公网不路由管理员 API/
master realm。内部证书仍需轮换；正式公网证书由 cert-manager 管理，身份网关
以只读文件挂载证书。还未提交用户密码或验收完整 Portal 登录/Dashboard。

新输入生成入口为 `.deployment/render_component_values.py`：`istio` 输入已
验证并部署；`platform` 遇到未批准来源或空模型价格会拒绝生成，不能把忽略的
环境文件传给强制要求 Entra 的 `make deploy`。初始登录信息只保存在受保护的
`.deployment/oidc-owner-login.json`，首次使用须修改临时密码，不在文档/日志中
显示。Keycloak/数据库/身份网关各单副本；OIDC Proxy 在当前单 System 节点使用
`.deployment/oidc-rollout.patch.yaml` 的 Recreate 策略，更新时会短暂中断登录，
避免滚动 surge 占满 CPU 预留并阻塞持久会话存储。已有应用未修改。

当前轮换后的 Token Plan Key 已录入并确认 Secret 存在；后续轮换时，在本机终端运行：

```bash
python3 .deployment/set_model_secret.py
```

以上命令从项目根目录执行。脚本拒绝管道输入和回显降级，核对目标 API 地址/
cluster UID，Key 仅经标准输入写入 Secret，不写进 argv、文件或 last-applied
annotation。6 项输入保护及超时保护的测试通过；脚本不发起模型推理，也不
自动重启 Gateway。Secret 更新后仍需批准的 Gateway rollout 和真实模型验收。

本机部署文件在 `../.deployment/cetirun.environment.yaml`，三个 Agent 在
`../.deployment/agents.yaml`，官方计价输入在 `../.deployment/billing.inputs.yaml`，
均不提交。GitHub Copilot 的默认/长上下文、缓存读写单价已核实记录，但还须
核对 Gateway 实际注册的计价表；不能把记录文件误认为已加载的预算策略。
已配置的阿里 endpoint 属于 **Token Plan**，不是旧 Coding Plan。官方文档列出了
两个指定模型，但按 Credits 动态抵扣，没有给出这两个模型的固定 USD/token
映射；月费除以额度不能代替模型单价。实际账单以 Ali Credits 为准，
CetiRun 的 USD budget/spend 是管理观测，不是上游账单。
用户提供的[模型调用计价](https://help.aliyun.com/zh/model-studio/model-pricing)
页面确实列出北京地域按量付费价格：Qwen 输入/输出为 0.8/2.7 元每百万 Token，
DeepSeek 忙时为 2/8、闲时为 1/4 元每百万 Token；忙时是北京时间 08:00–22:00。
已单独记录为 `payAsYouGoReference`，不是当前 Token Plan 的实际费率。
另以官方价格和 ECB 2026-11-06 汇率生成明确标注的 USD/token 管理参考估值，
已填入部署输入，保留拒绝无价模型保护。DeepSeek 使用忙时参考，缓存读不折扣、
缓存创建按输入参考值的 125%；这些缓存规则是管理估值，不是厂商该模型报价。
Portal 提示实际 Credits 账单与 USD 管理预算的区别；没有伪造 Credits/token
或 USD/Credit 映射。实际 Gateway 的数据库就绪与模型配置读取返回 HTTP 200，
两个模型的管理参考单价已核对；没有通过收费调用验收预算执行或协议兼容性。
Gateway 实际三个 Agent Virtual Key 的管理预算均为 USD 10/月；这是管理参考值，
不是 Ali Credits 配额。三个 Runtime 主工作负载各为单容器，另有独立隔离终端。
正式访问路径为 `/coding-copilot/term/`、`/coding-claude/term/`、`/coding-codex/term/`；
未登录请求均返回 302，完整本人会话和交互式编码仍需用户验收。
用户要求继续部署并保留 Token Plan；没有切换接口或发起模型收费调用。
个人版/团队版均要求本人/对应席位交互式工具使用，禁止共享及应用后端/自动化
调用；Gateway 转发模式须确认适用，已禁用共享模型的自动健康探测。

用户要求的 Claude Code + Qwen 是 Anthropic 协议第三方兼容路径，
不属于官方 Claude 模型支持声明；Gateway Messages/tool/stream 的真实兼容性
必须实测。Codex 的 DeepSeek 来源需要 Gateway 的 Responses-to-provider 转换验收。
Copilot 的指定模型需要完成平台 GitHub 授权并确认其真实 model ID/可用性；
不能用另一个模型充当它。所有模型名称以真实获准 catalog 为准。
