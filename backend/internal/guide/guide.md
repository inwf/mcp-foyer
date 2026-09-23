# mcp-foyer 使用指南

mcp-foyer 是一个 MCP 聚合端点：它连上若干个上游 MCP 服务器，把它们的工具与资源
合并成**一个** MCP 端点对外提供。客户端只需要配置一次，之后增删上游都不用
再动客户端。

## 目录

- [启动](#启动)
- [把客户端接上来](#把客户端接上来)
- [添加上游服务器](#添加上游服务器)
- [自带的系统工具](#自带的系统工具)
- [会话模式](#会话模式)
- [文件放在哪](#文件放在哪)
- [命令速查](#命令速查)

## 启动

```
mcp-foyer serve
```

不带子命令的 `mcp-foyer` 与 `mcp-foyer serve` 等价。默认监听 `127.0.0.1:7788`，
一个端口同时提供三样东西：

| 路径   | 用途                                  |
| ------ | ------------------------------------- |
| `/mcp` | 聚合后的 MCP 端点，客户端连这里        |
| `/api` | 管理 API，Web 界面与 CLI 用它          |
| `/`    | Web 界面（构建时嵌入的话）             |

首次启动不需要配置文件，它会以默认值运行；等你配置了第一台服务器才会写出
`config.yaml`。

只想换一次地址，不动配置文件：

```
mcp-foyer serve --port 9000
```

`--host` / `--port` 压过 `listen.host` / `listen.port`，**只影响本次运行**。
`--port 0` 表示向操作系统要一个空闲端口，实际端口在启动输出里报出。
`--host` 设成回环之外的地址，等于把管理 API 也一起暴露出去——它能改变哪些命令
会被当作子进程执行，所以那应当是一个明确的决定。

改配置前可以先离线检查一遍，这一步不需要 mcp-foyer 在跑：

```
mcp-foyer config validate
```

## 把客户端接上来

mcp-foyer 对外说的是 **streamable HTTP**。以 Claude Code 为例：

```
claude mcp add --transport http mcp-foyer http://127.0.0.1:7788/mcp
```

其他客户端如果只认 JSON 配置，写成这样：

```json
{
  "mcpServers": {
    "mcp-foyer": {
      "type": "http",
      "url": "http://127.0.0.1:7788/mcp"
    }
  }
}
```

上游服务器的工具会以 `服务器名_工具名` 的形式暴露出来（例如 `files_read`），
所以两台服务器各有一个 `read` 也不会撞名。

**但默认一个上游工具都不暴露。** 客户端一开始只看到 mcp-foyer 自己的四个系统工具，
上游工具要在配置里逐个点名（`mcpServers.<名字>.exposedTools`，Web 界面的工具页
每个工具有一个开关）才会进入 `tools/list`。

这是有意的：上游的 schema 很占地方，一台服务器十几个工具、每个十几个参数，
全塞进每个客户端的上下文就是纯浪费。**没暴露不等于用不了**——模型可以用
`search_tools` 找、`get_tool_details` 取 schema、`call_tool` 调，需要时才付这份 token。
把常用的几个暴露出来、其余留给按需检索，是 mcp-foyer 想要的用法。

想看有哪些还没暴露：

```
mcp-foyer tools list --all
```

## 添加上游服务器

上游只有两种，对应配置里 `transport` 的两个取值：

| `transport`       | 含义                                     |
| ----------------- | ---------------------------------------- |
| `stdio`           | 由 mcp-foyer 拉起一个子进程，走它的标准输入输出 |
| `streamable-http` | 连接一个已经跑在别处的服务                 |

拉起子进程的写法——命令放在 `--` 之后，这样它自己的参数不会被 mcp-foyer 抢走：

```
mcp-foyer servers add files -- npx -y @modelcontextprotocol/server-filesystem /tmp
```

连接已有服务的写法：

```
mcp-foyer servers add remote --url https://example.com/mcp \
  --header "Authorization=Bearer <token>"
```

两种写法都不用写 `--transport`：给了命令就是 `stdio`，给了 `--url` 就是
`streamable-http`。

常用选项：

| 选项            | 说明                                     |
| --------------- | ---------------------------------------- |
| `--env K=V`     | 子进程的环境变量，可重复                  |
| `--header K=V`  | HTTP 请求头，可重复                       |
| `--proxy URL`   | 走 HTTP 代理连接                          |
| `--timeout 30s` | 单次请求超时                              |
| `--disabled`    | 只写进配置，先不连                        |
| `--from-file f` | 从一份 YAML 片段读取完整定义              |

命令会等它连上再返回，所以启动失败当场就能看到，不用事后去翻。

改完之后：

```
mcp-foyer servers list
```

## 自带的系统工具

mcp-foyer 提供四个系统工具，按需发现上游能力。默认的 `tools/list` 只包含这四个，
配置 `exposedTools` 后还会包含被选中的上游工具。

| 工具 | 用途 |
| --- | --- |
| `list_servers` | 查看配置的服务器：描述、连接状态、工具与资源数量、失败原因 |
| `search_tools` | 按关键词搜索，或指定服务器分页浏览；可同时取得完整输入 schema |
| `get_tool_details` | 查看一个已知工具的完整输入 schema、title 与 annotations |
| `call_tool` | 按 `server` + `tool` 调用上游，`args` 放业务参数；转发前按 schema 校验 |

已知目标时直接搜索；已知服务器、工具和参数时直接调用，无需重复发现：

```text
search_tools(query="read file 读取文件", includeSchema=true, limit=2)
call_tool(server="files", tool="read", args={"path":"/tmp/example.txt"})
```

如果搜索时没有取 schema，可以再用 `get_tool_details(server="files", tool="read")`
补取。已暴露的工具也能直接按 `files_read` 这样的名字调用。
**未暴露的工具照样能被搜索、查看详情和调用。**

`search_tools` 的参数：

- `query`、`server` 至少提供一个非空值。只填 `server` 就是浏览该服务器。
- `query` 匹配工具名、工具描述、服务器名、握手名称、服务器描述，以及工具的参数名
  和参数描述（`dryRun` 能找到 `edit_file`）。名字按 `_`、`-` 和驼峰拆词，
  `readFile` 也能找到 `read_file`；中文按相邻两字匹配，`查询天气` 能找到
  「查询指定城市的天气」。多个词命中任意一个即入选，越少见的词权重越高，命中
  名字比命中描述权重高，参数权重最低；未命中的词列在 `unmatched`。查询要用目录
  所用的语言，目录可能中英混合，不确定时两种语言的词都写上。
- `limit` 默认 **5**，范围 **1–20**。显式传 0、负数或大于 20 会报错。
- `includeSchema` 默认 `false`；设为 `true` 时返回所选结果的完整 schema 和
  annotations，默认数量仍为 5。准备调用通常取 1–3 个候选即可，schema 不截断。
- 返回 `nextCursor` 表示还有下一页。保持 `query` 和 `server`，把它作为 `cursor`
  传回；可以改变 `limit` 和 `includeSchema`。

翻页检查本次查询的结果集合及顺序。无关服务器的变化不会让游标失效；若匹配结果
增减或重新排序，会要求从第一页重查。只改 schema 而顺序不变时，下一页读到最新详情。

搜索结果和工具详情都用 `server` + `tool` 标识工具；`exposed` 是可直接调用的
对外名，未暴露时为空。结果按 `score` 排序；`matched` 是命中的查询词数，只供参考。

mcp-foyer 自己的工具直接调用。要查看它们，用 `server="mcp-foyer"`：

```text
search_tools(server="mcp-foyer")
get_tool_details(server="mcp-foyer", tool="call_tool")
```

`call_tool` 只转发到配置中的上游，不调用 mcp-foyer 自己的系统工具。上游工具即使也叫
`search_tools` 或 `call_tool`，照样按指定的上游服务器转发。

`call_tool` 在转发前会把 `args` 对照该工具缓存的输入 schema 校验一遍：缺
required 字段、类型不对，会在 mcp-foyer 这里以可读的错误返回，不走一趟上游。它判断
不了的情况一律放行，由上游裁决：缓存里还没有这个工具（列表变更通知可能滞后）、
schema 解析不了、schema 声明了校验器不支持的版本。

这几件事简单的客户端未必看得到：`initialize.instructions`、资源。所以四个工具
的 description 各自把规则说全。`list_servers` 只报数量不报工具名，上百台服务器时
概览不能变成目录；看一台服务器有什么，用 `search_tools(server="名字")`。

资源 `foyer://guide` 是本文；`foyer://servers/{名字}` 返回服务器的状态、描述及全部
工具的「名字 → 描述」映射。不需要参数 schema 时，一次读取就能了解一台服务器。

这四个工具在 CLI 和 Web 中单独成组，也可以直接调用：

```text
mcp-foyer tools show search_tools
mcp-foyer tools call list_servers
mcp-foyer tools call search_tools --arg server=files --arg includeSchema=true --arg limit=2
```

服务器描述可在 Web 或配置文件中编辑。

## 会话模式

- **stateful**（默认）：每个客户端一个会话，支持服务端推送，工具列表变化会
  主动通知客户端。
- **stateless**：每个请求独立处理，`GET` 与 `DELETE` 被拒绝，没有通知。

选择顺序是：客户端请求头 `X-MCP-Session-Mode` > 配置里按 User-Agent 匹配的
`gateway.sessionModeRules` > `gateway.defaultSessionMode`。

**一条注意事项**：`gateway.keepAlive` 开启时，mcp-foyer 会周期性地向客户端发 MCP
`ping`。这要求客户端维持那条可选的 SSE 长连接（`GET /mcp`）。绝大多数客户端
——包括 Claude Code——默认就会维持它。若你的客户端明确关掉了这条流，请把
`gateway.keepAlive` 设为 `0`，否则它的会话会被判定为失联而关闭。

## 文件放在哪

所有东西都在**数据目录**里，默认是当前工作目录下的 `./data`：

```
data/
  config.yaml       配置
  logs/mcp-foyer.log   日志
  usage.json        工具使用计数（模型搜到、调用了哪些工具）
```

改用别处：`--data-dir /path` 或环境变量 `MCP_FOYER_DATA_DIR`。
只想换配置文件：`--config /path/to/config.yaml`。

想确认到底读了哪些路径：

```
mcp-foyer check
```

## 命令速查

```
mcp-foyer serve                        启动服务（不带子命令时的默认动作）
mcp-foyer serve --host H --port N      换一次监听地址，不写回配置文件
mcp-foyer check                        校验配置并报告各项路径，不监听端口
mcp-foyer config validate [file]       离线检查配置，一次报出全部问题

mcp-foyer servers list [--verbose]     列出服务器与各自状态
mcp-foyer servers add <name> ...       添加服务器
mcp-foyer status                       运行中实例的概况：连上了几台、在提供什么、谁连着

mcp-foyer tools list [--search 词]     列出/搜索 mcp-foyer 提供的工具
mcp-foyer tools list --all             连没暴露的一起列，并标出各自的对外名字
mcp-foyer tools show <工具>            看一个工具的完整说明与输入 schema
mcp-foyer tools call <工具> --arg k=v  调用一个工具

mcp-foyer ui [--print]                 用浏览器打开 Web 界面
mcp-foyer guide                        输出本文档
mcp-foyer version                      输出版本
```

以 `servers`、`tools` 开头的命令，以及 `mcp-foyer status` 与 `mcp-foyer ui`，
都是**运行中实例的客户端**——它们通过管理 API 询问那个实例，因为只有它知道自己
实际连上了哪些上游。默认从配置里的 `listen` 取地址，也可以用
`--address host:port` 指定。
