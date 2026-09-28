# mcp-foyer 对模型提供的接口

默认 `tools/list` 只有四个系统工具。配置 `exposedTools` 后，被选中的上游工具
也会出现在列表里；未暴露的工具仍可通过系统工具搜索、查看详情和调用。

## 禁用工具

`disabledTools` 中的工具不会出现在 `tools/list`、`search_tools` 或服务器资源的工具表中，
`list_servers` 和服务器资源的工具数量也不计入它们。点名请求详情或调用时返回禁用错误。
按发布名调用同样检查当前配置，即使客户端仍然持有旧列表也无法执行。被拒绝的调用不计入使用次数。

管理 API 的完整工具列表保留禁用记录，并带有 `disabled: true`；服务器视图提供
`disabledCount`。通过管理 API 调用禁用工具返回 HTTP 409，因此 Web 和 CLI 同样无法调用。

## 发现与调用

| 工具 | 输入 | 返回 |
| --- | --- | --- |
| `list_servers` | 无 | 配置的服务器名称、描述、状态、握手 title、工具/资源数量和错误 |
| `search_tools` | `query` 或 `server`，可加 `limit`、`includeSchema`、`cursor` | `hits`、可选 `nextCursor` 和 `unmatched` |
| `get_tool_details` | `server`、`tool` | 标识、对外名、描述、title、完整输入 schema、annotations |
| `call_tool` | `server`、`tool`、可选 `args` | 上游原始结果，包括业务错误；`args` 不符合 schema 时直接返回错误，不转发 |

已知目标时直接 `search_tools`，不必先列所有服务器。准备调用时可设置
`includeSchema=true`，一次搜索就获得参数定义；已知工具也能单独取详情。
已知 `server`、`tool` 和参数时直接调用，无需重复搜索。系统工具本身直接调用。

搜索和详情以 **`server` + `tool`** 标识一个工具，避免不同上游的短名冲突。
`exposed` 为可直接调用的发布名，未暴露时为空。上游工具即使与系统工具同名，
`call_tool` 也按指定的上游转发。

## 面向简单客户端的约定

mcp-foyer 服务的客户端不一定把 `initialize.instructions` 交给模型，也不一定支持
resources。因此：

- 四个工具的 description 各自自足，不依赖 instructions 或资源里说过的规则；
  模型可见的文本里统一只说「在/不在这个工具列表里」，自称「this endpoint」，
  不用产品名：客户端给这个服务器起什么名字由用户决定，模型不一定知道它叫 mcp-foyer。
- `list_servers` 只报每台服务器的工具与资源数量，不报工具名：上百台服务器时
  概览不能变成目录。一台服务器有什么，用 `search_tools(server)` 分页浏览。
- 服务器断连时的错误带上记录的原因（如 `command not found`），模型能转述给
  能修的人。
- `call_tool` 在转发前按该工具缓存的输入 schema 校验 `args`，缺 required 字段
  或类型不符时返回可读错误并指向 `get_tool_details`，不走一趟上游。校验只在
  能理解 schema 时进行：缓存里没有该工具、schema 解析失败、声明了不支持的
  JSON Schema 版本，都照常转发，上游仍是权威。
- 系统工具的结构化输出同时以 JSON 文本放在 `content` 里（Go SDK 的规范
  fallback），只读 `content` 的客户端拿到的是完整结果。

## 使用计数（对模型不可见）

mcp-foyer 记录模型对每个工具的使用：`search_tools` 返回给模型的那一页里出现的次数、
被调用的次数（`call_tool` 与按发布名直接调用两条路径合计）、其中失败的次数和
最近一次调用时间。**这些数字不进入任何系统工具的返回，也不进 instructions**，
它们是给 operator 看的，用来决定暴露哪些工具、改哪些描述。管理 API 与 Web 界面
发起的调用不计入。在转发前被拒绝的调用（未知服务器、参数不符）也不计，因为它没有
到达工具。

读取：`GET /api/gateway/usage`；清零：`DELETE /api/gateway/usage`。数据在
`data/usage.json`。

## 搜索参数与分页

| 参数 | 规则 |
| --- | --- |
| `query` | 可省略；匹配工具名、工具描述、服务器名、握手名称、服务器描述，以及输入 schema 里的参数名与参数描述。名字按 `_`、`-` 和驼峰拆词并折叠英文复数，中文按相邻两字匹配 |
| `server` | 可省略；准确的配置名，或 `mcp-foyer`；仅给此项时浏览单台服务器 |
| `limit` | 默认 5，范围 1–20；两种 schema 模式相同，显式 0 也拒绝 |
| `includeSchema` | 默认 false；true 时给所选候选附上完整输入 schema、title 和 annotations |
| `cursor` | 上一页的 `nextCursor`；必须配合相同的 query 和 server |

`query` 和 `server` 至少有一个非空值。多个关键词按 OR 匹配，以分字段 BM25 计分：
一个词在候选中越少见贡献越大，出现在工具名里的权重高于服务器名，再高于描述，
参数名与参数描述权重最低，长字段按长度折减。名字和参数名还以去掉分隔符的整体
形式参与匹配，所以 `readfile`、`readFile`、`read file` 都能精确命中 `read_file`，
`fullPage` 能精确命中同名参数。只按 `score` 排序，平局以服务器名和工具名稳定
排序；`matched` 报告命中的查询词数，不参与排序。没有匹配任何候选的词放在
`unmatched`，不抹掉其他词的结果。

查询要用目录所用的语言：中文查询打到纯英文的目录不会有结果。目录可能中英混合，
不确定时两种语言的词都写上，命中任一种即可。

```json
{"query":"read file","includeSchema":true,"limit":2}
```

```json
{
  "query":"read file",
  "hits":[
    {
      "server":"files",
      "tool":"read",
      "exposed":"",
      "description":"read a file",
      "matched":2,
      "score":1487,
      "inputSchema":{"type":"object","properties":{"path":{"type":"string"}}}
    }
  ]
}
```

示例只展示结果结构；具体 score 只在同一次搜索内可比，取决于目录里其他工具。
`nextCursor` 仅在还有结果时返回，最后一页省略。翻页可以改变 `limit` 或
`includeSchema`，但 query/server 必须保持相同含义；query 忽略大小写和词间空白。

游标只包含位置以及查询和有序结果标识的哈希，不在服务端保存快照。匹配结果增减、
顺序变化时拒绝旧游标，并提示去掉 cursor 重查；无关上游变化不影响它。schema
更新且顺序未变时，下一页返回最新详情。翻页过程中每次都读现有缓存，不发上游
发现请求，也不维护持久索引。

schema 不截断，也没有额外的 3 条硬上限或字节预算。需要控制响应大小时，调用方
可先取摘要或降低 limit；准备调用通常只需 1–3 个候选。详情继续不返回输出
schema；调用结果原样透传。

## 自身与资源

`search_tools(server="mcp-foyer")` 浏览这四个系统工具，
`get_tool_details(server="mcp-foyer", tool="call_tool")` 取得注册时生成的真实 schema。
跨上游搜索不会夹带系统工具。把 mcp-foyer 本身当上游调用时，会提示直接调用系统工具；
`call_tool` 的实际目标由上游配置决定。

- `foyer://guide` 是完整指南，与 `mcp-foyer guide` 共用一份文档。
- `foyer://servers/{名字}` 返回服务器状态、握手信息、描述及全部工具的名字到描述映射。
- 上游资源通过 `foyer://servers/{名字}/{转义后的上游 URI}` 读取。

服务器没有描述时省略该字段，不在每条结果中重复补写提示。服务器描述仍可从 Web
或配置维护。

`initialize.instructions` 会说明上述发现和调用路径，并指向指南；它和各工具的
description 用同一套措辞。测试把工具名与实际注册清单对照，防止文案在工具改名后
继续指向旧入口。

## MCP 互操作

Go 输出类型中的 schema 字段使用 `map[string]any`，避免生成布尔属性 schema
导致 TypeScript MCP SDK 拒绝整个工具列表。兼容性检查同时覆盖 `tools/list`
和带 schema 的搜索结果，不能只依靠同语言单元测试。
