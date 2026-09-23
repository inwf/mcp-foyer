# mcp-foyer

把若干个 MCP 服务器合并成**一个**端点。

它和常说的 MCP gateway 做的是同一类事，但它是给个人在本机用的单个二进制，没有
鉴权与策略层。重点是让不会自己做渐进式披露的简单客户端，也能按需搜索和调用工具。

客户端只配置一次，之后增删上游、改配置、看日志都在 mcp-foyer 这边完成，不用再碰
客户端。

```
Claude Code ─┐                        ┌─ stdio 子进程（filesystem、git…）
其他客户端  ─┼─→  mcp-foyer  /mcp  ───┼─ stdio 子进程
             ┘                        └─ streamable HTTP 服务（远端）
```

## 它解决什么

一个 MCP 客户端接十几个服务器时，会遇到三件麻烦事：

- **每个客户端都要配一遍。** 换一台机器、换一个客户端，就要重来。
- **上下文被工具塞满。** 几百个工具的完整 schema 一次性进上下文，代价不小。
- **出了问题看不见。** 某台服务器起不来时，客户端通常只是"那个工具不见了"。

mcp-foyer 对应的做法是：客户端只配一个地址；提供 `search_tools` / `get_tool_details` /
`call_tool` 这类系统工具，让模型按需发现工具，也能在一次搜索中取得调用所需的 schema；并给出一个 Web 界面
和一套 CLI，能看到每台服务器的状态、日志和它报的错。

## 快速开始

### Docker（不需要装 Go 和 Node）

```
cp docker-compose.example.yml docker-compose.yml
docker compose up -d
```

打开 <http://127.0.0.1:7788/> 是 Web 界面，把客户端指向
<http://127.0.0.1:7788/mcp>。停止用 `docker compose down`——配置和日志在一个命名
卷里，不会跟着消失。

`cp` 出来的文件不进版本库：发布端口取决于机器上还跑着什么，跟着仓库走只会让
改过的人多一份没人想要的改动。

镜像里带了 Node 与 npm，所以 `npx` 那一类 stdio 上游可以直接配，不用再套一层
自己的镜像。详见 [Docker 一节](#docker)。

### 从源码构建

需要 Go 1.25+ 和 Node 22+（含 pnpm）。

```
make build          # 构建前端，嵌入后端，产出单个二进制
./backend/bin/mcp-foyer serve
```

同样是 <http://127.0.0.1:7788/> 和 <http://127.0.0.1:7788/mcp>。

以 Claude Code 为例：

```
claude mcp add --transport http mcp-foyer http://127.0.0.1:7788/mcp
```

加一台上游服务器：

```
mcp-foyer servers add files -- npx -y @modelcontextprotocol/server-filesystem /tmp
mcp-foyer servers list
```

完整用法：`mcp-foyer guide`。配置字段：[docs/configuration.md](docs/configuration.md)。
mcp-foyer 对模型说了什么：[docs/mcp-surface.md](docs/mcp-surface.md)。

## 有什么

- **一个 MCP 端点**聚合全部上游。工具以 `服务器名_工具名` 暴露，两台服务器各有
  一个 `read` 也不会撞名。
- **两种上游传输**：`stdio`（由 mcp-foyer 拉起子进程）与 `streamable-http`（连接
  已在运行的服务）。
- **四个系统工具**，让模型按需检索工具而不是全量加载。
- **Web 界面**：服务器状态、工具与资源浏览、实时日志、配置编辑。
- **CLI**：`servers` / `tools` / `ui` / `config validate` / `guide`。
- **单个二进制**。前端在构建时嵌入，部署时不需要另外准备静态文件。
- **所有产生的文件都在数据目录内**（默认 `./data`），不往主目录里散落东西。

## 构建与开发

```
make help           # 列出全部目标
make build          # 前端 → 嵌入 → 单个二进制
make check          # 两侧的全部检查
make dev            # 打印开发时怎么把两半跑起来
make clean          # 清理构建产物
```

开发时前后端分开跑：

```
cd backend  && go run ./cmd/mcp-foyer serve
cd frontend && pnpm dev
```

开发服务器会把 `/api`、`/ws`、`/mcp` 代理到后端，并**保留页面自身的来源**——
这样后端的 WebSocket 来源检查是被真正走了一遍，而不是被绕过去。

工具发现的基准、实测数据和跨语言 SDK 复测命令见 [性能与兼容性验证](docs/performance.md)。

`make check` 包含：

| 一侧 | 内容                                             |
| ---- | ------------------------------------------------ |
| Go   | `gofmt` 检查、`go vet`、`go test -race ./...`      |
| Web  | TypeScript 类型检查、oxlint、Vitest               |

**Web 界面是构建期的选择，不是运行期的。** 后端用 `webui` 构建标签决定是否嵌入
前端；不带这个标签构建出来的二进制照常提供 API 与 MCP 端点，只是没有界面。
`make build` 会带上它。

## Docker

```
cp docker-compose.example.yml docker-compose.yml   # 第一次，只需一次
docker compose up -d          # 起来（第一次会构建镜像）
docker compose logs -f        # 看日志
docker compose down           # 停掉，数据卷保留
docker compose down -v        # 停掉，并且删掉配置与日志
```

**仓库里只有 `docker-compose.example.yml`，`docker-compose.yml` 在 `.gitignore`
里。** 需要改的发布端口因机器而异，跟着仓库走的话，谁改了都会得到一个脏的工作区
和一份别人不想要的改动。`docker-compose.override.yml` 也一并忽略了，习惯用 compose
的覆盖机制的话可以直接用。

镜像是三段构建，跟 `make build` 同一个顺序：Node 构建前端 → 拷进嵌入目录 →
带 `webui` 标签构建二进制。跑起来的那一层不带任何工具链。

**镜像里装了 Node 与 npm。** 最常见的 stdio 上游是 `npx` 包，一个起不动这些
服务器的 mcp-foyer 得再套一层自己的镜像才能用——所以这是刻意把镜像做大的唯一一处
（约 154 MB）。加服务器和平时一样：

```
docker compose exec mcp-foyer mcp-foyer servers add files -- \
  npx -y @modelcontextprotocol/server-filesystem /tmp
```

### 两件需要知道的事

**谁能访问，由发布端口这一道决定。** 默认只发布到宿主机回环
（`127.0.0.1:7788:7788`），也就是只有这台机器上能连。管理 API 能改变哪些命令会被
作为子进程执行，所以这道边界值得明确设。内网部署、能连到即可信的场景，改成
`"7788:7788"` 即对整个内网开放。

**容器里不再用 IP 白名单挡。** mcp-foyer 自带的 `security.allowedNetworks` 默认只放行
回环，而发布进容器的请求源地址是容器网关（不是回环），用默认会把所有人挡在外面、
连 Web 界面都是 403。所以入口脚本在**第一次启动**时写一份 `config.yaml`，把这个
白名单设成空列表——mcp-foyer 读作「放行所有来源」。对容器来说，访问边界是上面那个
发布端口，不是这个列表。此后该文件不再被脚本改动；若需按来源收紧，把
空列表换成 CIDR 段（比如 `[10.0.0.0/8]`）即可。

**改监听地址与会话规则要重启才生效。** 容器里也一样，`docker compose restart`。

## 目录结构

```
backend/
  cmd/mcp-foyer/        CLI：命令树、serve、各子命令、内嵌使用指南
  internal/
    config/          配置的类型、解析、校验、读写
    logging/         日志、轮转、供界面查询的内存缓冲
    upstream/        上游连接：拨号、状态、重连
    gateway/         聚合的 MCP server、系统工具、会话模式
    api/             管理 API、WebSocket 事件流、静态资源
    events/          事件总线
    webui/           嵌入前端产物（受构建标签控制）
    testmcp/         测试用的真实 MCP 服务器，两种传输共用
    integration/     跨包的端到端测试
frontend/
  src/
    api/             HTTP 客户端、事件流、类型
    pages/           各页面
    components/      共用组件
    layout/          外壳与导航
    hooks/ stores/   事件流订阅与客户端状态
    theme/ styles/   设计令牌与主题
    i18n/ lib/       文案与工具函数
    test/            测试夹具
docs/
  configuration.md   配置字段参考
  mcp-surface.md     对 MCP 客户端的自述
```

## 技术选型

**后端** Go 1.25 · [官方 MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)
· gin · gorilla/websocket · goccy/go-yaml · cobra · 标准库 `log/slog`

**前端** React 19 · TypeScript 6（`strict` 及五个附加严格标志全部显式开启）·
Vite 8 · antd 6 · TanStack Query 5 · Zustand 5 · React Router 7 · Vitest + msw ·
oxlint
