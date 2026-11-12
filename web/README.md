# 任务游戏 Web

给发布者和执行者用的本地演示界面。文案是中文，任务 id、状态和字段名保持英文。页面通过 [`sdk/ts`](../sdk/ts)（npm 包名 `taskgame-client`）调用本机 API，不走 registry。

## 运行

终端 1，在仓库根目录启动 API：

```bash
make run
# http://127.0.0.1:8891
```

终端 2：

```bash
cd web
npm install
npm run dev
```

浏览器打开 `http://localhost:5173`。未登录时停在注册/登录页。注册或登录成功后，Bearer JWT 和用户 id 写进 `localStorage`，之后的任务请求带 `Authorization: Bearer`。Vite 如果 5173 被占用，会顺延到 5185 以内；`npm run preview` 使用 4173。服务只给这些本机源回 CORS 头：`localhost`、`127.0.0.1`、`[::1]` 的端口 4173 和 5173–5185。`POST /v1/register`、`POST /v1/login` 和 `GET /v1/me` 走同一套 CORS。

```bash
cd web
npm install
npm run build
```

`npm run build` 会先编译 `../sdk/ts`，再做类型检查和 Vite 打包。

## 账号

打开页面先注册或登录。密码至少 8 个字符。服务签发的 JWT 与 `make token` 相同（HS256，`sub` 为 `usr_…`）。侧栏显示邮箱和用户 id，退出登录会清掉本机令牌。

侧栏和登录页里的「开发快捷身份」仍可切回静态密钥：

`etc/taskgame.yaml` 里的开发密钥：

| 角色 | `X-Api-Key` | 用户 id |
| --- | --- | --- |
| 发布者 | `dev-publisher-key` | `usr_publisher` |
| 执行者 | `dev-executor-key` | `usr_executor` |
| 裁定者 | `dev-arbiter-key` | `usr_arbiter` |

展开后，两个角色按钮会写回对应的 API Key 和用户 id。Base URL、API Key、用户 id 都可以改。第二个执行者没有静态密钥时：

```bash
make token USER_ID=usr_extra
```

把鉴权改成 Bearer，贴上 JWT，用户 id 填 `usr_extra`。`make` 会先回显命令，整段输出可以一起贴，页面会从中取出 JWT。界面上的用户 id 用来决定按钮和默认聊天对象，真正的调用身份仍是密钥或 JWT 的 `sub`，两边要一致。

## 环境变量和 localStorage

| 键 | 作用 |
| --- | --- |
| `VITE_TASKGAME_BASE_URL` | 还没有保存过设置时，作为 Base URL 的初始值 |
| `taskgame.web.settings` | JSON：`baseUrl`、`role`、`authMode`（`apiKey` 或 `bearer`）、`apiKey`、`bearerToken`、`userId`、`email`。没有密钥也没有 JWT 时停在登录页 |
| `taskgame.web.chats` | 本浏览器创建或记下的会话，以及 `postMessage` 返回的消息 |

任务详情会读取绑定会话的历史（`GET /chats/{id}/messages`，页内从旧到新）。侧栏「邀请」列出 `GET /me/invites?status=pending`。任务列表的「可接任务」使用 `assignable=me`。创建页可以选择开放、能力门槛或指定人。

## 三条任务

金额是模拟积分。创建页的报酬模式是 `one_shot`。承接策略可选开放、能力门槛或指定人。任务详情会显示提交、下一步、截止时间、请对方修改、取消、协商和（裁定者登录时）裁定。分别结算的席位仍用「退回该席」和「确认拨付」。

1. **exclusive**：发布者创建并注资，执行者接受、提交，发布者确认。确认体是 `{}`，结束后状态为 `settled`。
2. **competitive**：`settlement_rule` 固定 `winner_take_all`。两个执行者都接受并提交后，发布者先不带 `submission_id` 确认。400 的 `message` 交给 SDK 的 `submissionIdsFromConfirmError`，页面按 `created_at` 顺序列出 id，选定后再确认。
3. **collaborative**：`on_dropout` 固定 `replace`。各席 `share_bps` 合计 10000。执行者接受时选择展开后的 `role_id`。退出后该席回到 `open`，佣金和能力分为 0。再用同一个 `role_id` 接受，就是补位。

   创建页按「角色 × 人数」填写，同一种角色不用逐行加席。每一行是一种角色：显示名称、可选基础 id、人数。未勾选「手动调整各席 share_bps」时，请求发送角色模板：`quantity` 是人数，`share_bps` 是这一角色的合计（各角色相加为 10000，人数不乘进去）。服务端按人数展开；除不尽时，多出来的基点加在该角色最后几席。人数为 1 时席位 id 就是基础 id，大于 1 时是 `design_1`…`design_4`。例如 4×设计（基础 id `design`）和 2×文案（`copy`）展开成 `design_1`…`design_4` 与 `copy_1`、`copy_2`。10000 先在全部席位间均分，再按角色合计交给服务端，预览和结算一致。基础 id 留空时，名称若已是合法 slug 就用名称，否则按顺序用 `role`、`role2`、`role3`。勾选手动调整后，请求改为逐席的扁平 `roles[]`（省略 `quantity`），合计仍须是 10000。

积分页读 `GET /v1/users/{userId}/scores` 的 `integrity` 和 `ability`。

## Run

```bash
make run
cd web && npm install && npm run dev
```

Open `http://localhost:5173`. Register or log in with email and password; the JWT and user id are stored in `localStorage` and sent as Bearer. The collapsed「开发快捷身份」panel still switches `dev-publisher-key` (`usr_publisher`) and `dev-executor-key` (`usr_executor`). Settings live in `taskgame.web.settings`. Optional `VITE_TASKGAME_BASE_URL` applies only before that key exists. Chat transcripts are local (`taskgame.web.chats`) because v0.1 has create and post, not list.
