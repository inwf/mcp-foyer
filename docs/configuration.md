# 配置参考

配置是**一个 YAML 文件**，默认在 `<数据目录>/config.yaml`。

两条贯穿全文的规则：

- **时长写成带单位的字符串**，如 `30s`、`5m`、`2m30s`、`168h`，不写裸数字。单位
  永远不含糊。
- **不认识的键是错误，不是被忽略的东西。** 把 `port` 拼成 `prot` 会导致启动
  失败并指出位置，而不是悄悄用回默认值——后者要到你纳闷"我明明改了"的时候才
  会被发现。

改完之后可以先离线检查，这一步不需要网关在跑：

```
mcphub config validate
```

它一次报出全部问题，不是报一个停一个。

## 一份完整的默认配置

下面这份就是首次启动时生效的内容（没有配置文件时它不会被写出来，等你配置了
第一台服务器才会落盘）：

```yaml
version: 1
listen:
  host: 127.0.0.1
  port: 7788
logging:
  level: info
  format: console
  maxAge: 168h0m0s
  maxSizeMB: 50
  mcpWireDebug: false
  apiDebug: false
security:
  allowedNetworks:
    - 127.0.0.1/32
    - ::1/128
  maxConnections: 50
  maxConcurrentRequests: 50
  connectionTimeout: 30s
  idleConnectionTimeout: 5m0s
gateway:
  defaultSessionMode: stateful
  sessionModeRules: {}
  sessionTimeout: 30m0s
  notifyDebounce: 3s
  keepAlive: 30s
  keepAliveFailureThreshold: 3
startup:
  connectDelay: 3s
  maxRetries: 3
  retryBackoff: 5s
mcpServers: {}
```

## `version`

| 字段      | 默认 | 说明                                                  |
| --------- | ---- | ----------------------------------------------------- |
| `version` | `1`  | 配置的 schema 版本。存在的意义是让将来不兼容的改动能被**发现**，而不是被误读 |

## `listen`

一个端口同时提供 MCP 端点、管理 API 与 Web 界面。

| 字段   | 默认        | 说明                                         |
| ------ | ----------- | -------------------------------------------- |
| `host` | `127.0.0.1` | 监听地址                                      |
| `port` | `7788`      | 监听端口。`0` 表示向操作系统要一个空闲端口     |

**默认只监听回环地址是刻意的。** 管理 API 能改变哪些命令会被作为子进程执行，
所以让它可达必须是一个明确的决定，而不是默认值的副作用。

`port: 0` 时端口在启动时才确定、只在启动输出里出现，配置文件里查不到——此时
CLI 的客户端类命令需要 `--address host:port`。

**这两个字段可以在命令行上被压过一次**：`mcphub serve --host H --port N`
（不带子命令的 `mcphub --host H --port N` 也一样）。**只影响本次运行，不写回
配置文件**——命令行标志的含义就是「这一次」。值按同一套规则校验，不合法时报的是
用法错误（退出码 2），并且指名说的是 `--host` 还是 `--port`。

不读 `HOST` / `PORT` 环境变量：这两个名字在容器与 CI 里常被别的东西占着，一个
本地网关因为环境里恰好有个 `PORT` 就换了端口，比不支持更难查。

## `logging`

日志文件写在数据目录下的 `logs/`。

| 字段               | 默认        | 说明                                    |
| ------------------ | ----------- | --------------------------------------- |
| `level`            | `info`      | `debug` / `info` / `warn` / `error`      |
| `format`           | `console`   | `console`（给人看）或 `json`（给机器看） |
| `maxAge`           | `168h0m0s`  | 轮转后的日志保留多久                     |
| `maxSizeMB`        | `50`        | 单个日志文件多大时轮转                   |
| `mcpWireDebug`     | `false`     | 记录双向的 MCP 原始报文。很吵，排查协议层问题时才开 |
| `apiDebug`         | `false`     | 记录管理 API 的请求与响应体              |
| `gatewayDebug`     | `false`     | 只把**网关自己**的动作记到 debug：发布/撤下了哪些工具、转发成功与失败 |
| `showTraceContext` | `true`      | 输出里带上 `requestId` 与 `session`      |

**`gatewayDebug` 与把 `level` 调成 `debug` 不是一回事。** 后者是钝器：整个程序的
debug 一起出来，每台上游服务器的絮语和每个 HTTP 请求都在里面，你要找的那一行从
中间划过去。`gatewayDebug` 只放网关这一个模块。

**`showTraceContext` 只影响控制台与文件，不影响日志页。** 关掉它是让每行短一点、
便于扫读；内存里的日志缓冲两种情况下都保留全部字段，所以 Web 界面照样能按
`requestId` 或 `session` 筛。

## `security`

| 字段                    | 默认                            | 说明                          |
| ----------------------- | ------------------------------- | ----------------------------- |
| `allowedNetworks`       | `["127.0.0.1/32", "::1/128"]`   | 允许连接的 CIDR 列表           |
| `allowedOrigins`        | *(不写)*                        | 额外允许打开事件流的浏览器来源  |
| `maxConnections`        | `50`                            | 同时打开的 TCP 连接上限         |
| `maxConcurrentRequests` | `50`                            | 同时处理的请求上限              |
| `connectionTimeout`     | `30s`                           | 单连接的无活动超时              |
| `idleConnectionTimeout` | `5m0s`                          | 长连接流的无活动超时            |

三处需要注意：

- **`allowedNetworks` 为空列表表示放行所有客户端**，与"这个键不写"（拿到默认的
  回环白名单）意思相反。这是本 schema 里少数几个"空值 ≠ 缺省"的地方之一。
- **`idleConnectionTimeout` 必须大于 `connectionTimeout`**，否则每个安静的客户端
  都会在流中途被断开。校验会拒绝违反此关系的配置。
- 单独写一个 `allowedOrigins` 的原因：WebSocket 握手不受普通请求那套跨源检查的
  保护。不加限制的话，你访问的任何网站上的页面都能连上你自己机器上的这个端口
  并读走全部事件。空列表表示"仅同源"，也就是自带 Web 界面所需要的；把前端跑在
  独立的开发服务器上，才是需要往这里加条目的场景。

## `gateway`

| 字段                        | 默认        | 说明                                       |
| --------------------------- | ----------- | ------------------------------------------ |
| `defaultSessionMode`        | `stateful`  | `stateful` 或 `stateless`                   |
| `sessionModeRules`          | `{}`        | 按 User-Agent 子串选择模式，见下             |
| `sessionTimeout`            | `30m0s`     | 空闲这么久的会话过期                        |
| `notifyDebounce`            | `3s`        | 合并上游工具/资源变化的抖动窗口              |
| `keepAlive`                 | `30s`       | 向客户端发存活探测的间隔。`0` 关闭            |
| `keepAliveFailureThreshold` | `3`         | 连续失败多少次判定会话失联                   |

`sessionModeRules` 的写法：

```yaml
gateway:
  sessionModeRules:
    stateless:
      - curl
      - python-requests
```

匹配不区分大小写。同一模式下命中最长的关键词生效；两边一样长时算 `stateful`。
优先级：客户端请求头 `X-MCP-Session-Mode` > 这里的规则 > `defaultSessionMode`。
两边都留空就只用默认模式。

**只在下一次启动时生效。** 这些规则在网关启动时读进内存，运行中改文件或改设置
页都不会影响当前实例（监听地址也是这样）。

**`keepAlive` 有一个前提需要知道。** 它是**服务端向客户端发 MCP `ping`**，这
要求客户端维持那条**可选的** SSE 长连接（`GET /mcp`）。绝大多数客户端默认都会
维持它。若你的客户端明确关掉了这条流，它的会话会被判定失联而关闭——此时应把
`keepAlive` 设为 `0`，改由 `sessionTimeout` 回收。

## `startup`

启动时如何把上游一台台带起来。

| 字段           | 默认  | 说明                                        |
| -------------- | ----- | ------------------------------------------- |
| `connectDelay` | `3s`  | 逐台连接之间的间隔，避免一次拉起大量子进程    |
| `maxRetries`   | `3`   | 连接失败重试次数                             |
| `retryBackoff` | `5s`  | 指数退避的基准间隔                           |

## `mcpServers`

以名字为键。名字会直接用在聚合后的工具名和资源 URI 里，所以有约束：

- 只能是 `^[A-Za-z0-9][A-Za-z0-9_-]*$`（首字符是字母或数字，其余可含下划线和
  连字符），最长 64 个字符。
- 不允许空格、点、斜杠——它们在工具名和 URI 路径里有别的含义；开头的连字符会被
  CLI 读成标志。

| 字段           | 适用          | 默认      | 说明                                        |
| -------------- | ------------- | --------- | ------------------------------------------- |
| `transport`    | 全部          | `stdio`   | `stdio` 或 `streamable-http`                 |
| `enabled`      | 全部          | `true`    | 为 `false` 时保留在配置和界面里，但不连接      |
| `description`  | 全部          | *(空)*    | 界面上显示，也会返回给模型帮助它选服务器       |
| `timeout`      | 全部          | `60s`     | 单次请求超时                                 |
| `exposedTools` | 全部          | *(空)*    | 只暴露列出的工具。**不写与写成空列表含义相同：一个都不暴露**，见下 |
| `command`      | stdio         | —         | 要执行的程序，**必填**                        |
| `args`         | stdio         | *(空)*    | 命令行参数                                   |
| `env`          | stdio         | *(空)*    | 追加的环境变量。是**叠加**不是替换，子进程仍能拿到 `PATH`、`HOME` |
| `readyPatterns`| stdio         | *(空)*    | 正则列表，匹配子进程写到 stderr 的行；命中任意一条才开始握手 |
| `readyTimeout` | stdio         | `30s`     | 等待上面那些模式的上限。**超时不是失败**，见下  |
| `url`          | streamable-http | —       | 服务端点，**必填**                            |
| `headers`      | streamable-http | *(空)* | 每个请求都会带上的请求头，如 `Authorization`   |
| `proxy`        | streamable-http | *(空)* | 走 HTTP 代理连接。不写则沿用 `HTTP_PROXY` 等环境变量 |

**两种传输各自需要恰好一个"地址"，写错另一个是错误而不是被忽略：** stdio 必须有
`command` 且不能有 `url`/`proxy`；streamable-http 必须有 `url` 且不能有 `command`。
在 stdio 服务器上写 `url`，最可能的情况是传输类型选错了——静默丢弃它会让 mcphub
连到一个不是你想要的地方去。

**`exposedTools` 的默认值是"什么都不暴露"，这是刻意的。** 不写这个字段、或者写成
空列表，含义完全相同：这台服务器的工具**一个都不会出现在** `tools/list` 里，客户端
连上来看到的只有网关自己的四个系统工具。

理由是上下文开销。一台服务器十几个工具、每个工具十几个参数，几台服务器就是几百个
字段的 schema，而它们会进入**每一个**客户端会话的上下文——不管这次会话用不用得上。
所以默认不给，需要哪几个就在这里点名（Web 界面工具页的每个工具有一个开关，效果
相同）。

**没暴露不等于用不了，这是这个设计成立的前提。** 未暴露的工具照样能被找到、能看
schema、能调用：

```
search_tools(includeSchema=true)  →  call_tool
```

`search_tools(server="名字")` 与 `hub://servers/{名字}` 也能浏览未暴露的工具。
已知工具时可用 `get_tool_details` 单独取详情。差别只在于它们不占
`tools/list` 的位置——**"不在列表里"和"不能用"是两件事**。

想知道哪些还没暴露：`mcphub tools list --all`（未暴露的那一列是 `-`）。
模型这一侧怎么被告知这件事，写在 [`mcp-surface.md`](mcp-surface.md) 里；
面向使用者的完整流程见 `mcphub guide`。

**`readyPatterns` 是给那些"要先说一句话才能应答"的服务器用的**（例如先打一行
`listening on ...`，或者先自己装一遍依赖）。不写就不等，握手立刻开始——绝大多数
服务器要的是这个，所以这两个字段留空是正常状态。

**超时之后照样握手，只是记一条警告。** 这是刻意的：模式写错一个字比服务器真的很慢
常见得多，而"超时即失败"会把一个拼写错误变成一台永远连不上的服务器。等待用尽后落回
`startup` 那边的 `maxRetries` / `retryBackoff` 重试路径，两个机制是串联的。

注意这个等待**每次连接、每次重试都要全额付**，所以 `readyTimeout` 调大之前先确认
模式真的能匹配上——`readyPatterns` 是正则，`.` 和 `(` 都有含义。模式编译不过、写成
空串、或者只写了 `readyTimeout` 而没有模式，都会在保存前被拒绝并指名字段，而不是等
到启动才炸。

两个例子：

```yaml
mcpServers:
  files:
    transport: stdio
    command: npx
    args:
      - -y
      - "@modelcontextprotocol/server-filesystem"
      - /tmp
    timeout: 30s

  remote:
    transport: streamable-http
    url: https://example.com/mcp
    headers:
      Authorization: Bearer <token>
    timeout: 60s
```

命令行等价写法见 `mcphub guide --section 添加上游服务器`。

## 数据目录

配置、日志与运行时状态都在数据目录里，**不会写到主目录**：

```
data/
  config.yaml
  logs/mcphub.log
  usage.json
```

`usage.json` 是工具使用计数：模型通过 `search_tools` 搜到、通过 `call_tool` 或
发布名调用了哪些工具，各多少次、失败几次。改动后几秒内写一次，退出时再写一次；
文件损坏时从零开始计并写一条警告，不影响启动。Web 界面的工具页显示这些数字并
可清零，也可直接删掉这个文件。

按以下顺序决定它的位置：

1. `--data-dir` 参数
2. `MCPHUB_DATA_DIR` 环境变量
3. 当前工作目录下的 `./data`

只想换配置文件而不换数据目录：`--config /path/to/config.yaml`。

想确认实际用了哪些路径：`mcphub check`。
