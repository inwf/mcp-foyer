# MCP Foyer 界面精修

分支：`design/unslop-console`。修改覆盖总览、服务器、工具、资源、日志、设置、服务器详情及共享弹窗，浅色与深色共用同一套尺寸规则。

[查看 10 秒改造视频](../unslop-story.mp4)

## 设计方向

- 产品：为个人开发者管理多个 MCP 服务与工具的控制台
- 性格：沉稳、精确、温和
- 环境：本地开发工作台，延续暖灰与陶土色
- 重点：工具目录、连接状态与真实调用信息
- 识别细节：多路汇入一个端点的原有标志，以及统一尺寸的状态圆点

## 审查与修改

原界面已有明确的品牌和页面结构，本次按精修方式处理。

| 步骤 | 发现与修改 |
| --- | --- |
| 间距 | 侧栏从 216px 调整为 240px，导航使用 36px 行高和 4px 间距；面板、筛选栏、统计卡和列表使用统一间距 |
| 颜色 | 保留浅色与深色的暖灰、陶土色；导航选中使用中性表面和细边框，强调色集中在图标与主要操作 |
| 图标 | 保留 Ant Design 图标；导航、折叠箭头和操作按钮使用 16px 图标，状态圆点统一为 8px |
| 文案 | 缩短主要页面说明，明确 MCP 端点、工具可见范围与调用；去掉短说明的句末标点 |
| 字体 | 页面标题使用 32px / 500，手机为 28px；辅助信息提高至 12px，列表主体为 14px；数字使用等宽数字，保留原有系统字体 |
| 布局 | 保留所有页面与内容顺序；240px 侧栏搭配流动内容区，手机保留底部导航；修正 320px 下服务器筛选栏溢出 |
| 细节 | 控件圆角 8px、面板圆角 12px、常规控件高度 36px；简化数量标签，区分工具可见状态和键盘焦点；表格焦点显示在容器内部 |
| 视觉 | 使用真实 MCP 实例及官方 filesystem 服务读取项目文档；工具、连接数量与状态均来自实际响应 |
| 动效 | 统一缓动曲线，列表延迟最多五个间隔；减少动态效果偏好下关闭加载骨架动画 |

## 视频内容

1920 × 1080，60fps，10 秒。完整呈现 1440 × 810 的应用窗口。

- 0–6 秒：工具页在同一份 DOM 上连续改变间距、导航与图标、文字、控件细节，红线标记修改区域
- 6–7 秒：深色工具目录
- 7–8 秒：浅色总览
- 8–9 秒：深色服务器列表
- 9–10 秒：深色设置

`story.html` 是独立的演示页面，`steps.css` 记录四个步骤。发布界面使用 `frontend/src` 的 React 与 CSS，演示文件独立于应用构建。

录制素材来自独立测试实例，服务器名称为 `project-files`，读取范围为项目 `docs` 目录。

## 验证

```sh
VITEST_MAX_WORKERS=2 make check
make build
```

检查包括 Go 格式、vet、竞态测试，前端类型检查、oxlint 和 194 项 Vitest 测试。

浏览器使用真实 API 验证工具搜索、系统与上游工具调用、卡片与列表切换、分组折叠、主题持久化、服务器编辑、设置重置和 YAML 未保存提示；检查 320、390、768、1024、1440px 下六个主要页面的横向溢出和键盘跳转。

## 重新生成视频

需要 Node.js、Playwright、Chromium 和 ffmpeg。在安装了 Playwright 的目录运行：

```sh
node /path/to/mcp-foyer/unslop-story/render.mjs /path/to/mcp-foyer/unslop-story/story.html /path/to/mcp-foyer/scratch/morph-wide.mp4 --wide
node /path/to/mcp-foyer/unslop-story/compose.mjs
```

可以通过 `CHROME_PATH` 指定 Chromium 可执行文件。

## 署名

设计流程与 `render.mjs` 改编自 [Unslop](https://heydamir.design/unslop)，作者 [Damir Sokolovsky（@heydamir）](https://x.com/heydamir)，遵循 [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)。改编内容包括全窗口横屏取景、六秒连续变化片段及多个真实页面的合成；署名随视频在本说明中保留。
