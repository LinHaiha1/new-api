# NewAPI 注册 ALTCHA 验证

该目录提供一个独立的注册验证网关，不修改 NewAPI 注册后端和数据库。

## 保护范围

- 注册页面：`/register`、`/sign-up`
- 注册接口：`/api/user/register`
- 登录、控制台、API 中转和其他接口不经过验证

流程：

```text
访问注册页
-> ALTCHA 页面
-> 浏览器执行 SHA-256 PoW
-> gate 校验签名、有效期、防重放和限频
-> 签发 5 分钟 HttpOnly Cookie
-> Nginx auth_request 放行注册页和注册接口
```

ALTCHA 只能提高自动注册成本，不能保证请求一定来自真人。生产环境仍建议配置
Cloudflare、源站限频和异常注册监控。

## 前端资源

ALTCHA `3.2.1` 的国际化前端文件已固定在
`gate/static/altcha.i18n-3.2.1.min.js`，并由 gate 通过
`/auth-gate/assets/` 提供。运行时不再访问 jsDelivr 或其他前端 CDN。

- 来源：npm 包 `altcha@3.2.1`
- SHA-256：`67a06fef795b716022fc0635346fb4a3ba433d8b8eb429044e2b7d14edd9bc4e`

对应的 MIT 许可证保存在 `gate/licenses/ALTCHA-MIT.txt`，并随容器镜像
复制到 `/licenses/ALTCHA-MIT.txt`。更新 ALTCHA 时应同时更新版本化文件名、
HTML 引用和许可证。

## NewAPI 前端改动

经典前端和新版前端的注册入口使用普通 `<a>` 跳转，让浏览器向 Nginx
发起完整页面请求。登录仍保留原来的 SPA 跳转。

## 部署

1. 将 `docker-compose.altcha.example.yml` 合并到现有 Compose。
2. 现有 `newapi` 服务不要再直接发布 `13333:3000`，只在 Compose 网络暴露
   `3000`。公网 `13333` 由 `newapi-auth-gateway` 发布。
3. 如果服务名不是 `newapi`，同步修改 `nginx-altcha.conf` 中的上游名称。
4. 生成两个不同的随机密钥：

```bash
openssl rand -hex 32
openssl rand -hex 32
```

5. 在服务器的 `.env` 中配置，禁止提交到 Git：

```dotenv
ALTCHA_HMAC_SECRET=替换为第一个随机值
ALTCHA_COOKIE_SECRET=替换为第二个随机值
ALTCHA_COOKIE_SECURE=true
ALTCHA_MAX_NUMBER=100000
NEWAPI_PUBLIC_PORT=13333
```

使用 HTTPS 时必须设置 `ALTCHA_COOKIE_SECURE=true`。仅在本机 HTTP 测试时
设置为 `false`。

6. 构建并启动：

```bash
docker compose -f deploy/altcha/docker-compose.altcha.example.yml up -d --build
```

如果是合并到已有 Compose，则使用实际的 Compose 文件执行
`docker compose up -d --build`。

## 1Panel / 反向代理

1Panel 或 OpenResty 应代理到宿主机 `127.0.0.1:13333`，不要绕过网关直接
代理到 NewAPI 容器的 `3000` 端口。

## 验收

```bash
curl -I http://127.0.0.1:13333/login
curl -I http://127.0.0.1:13333/register
curl -I http://127.0.0.1:13333/sign-up
curl -i -X POST -H "Content-Type: application/json" \
  -d '{}' http://127.0.0.1:13333/api/user/register
```

预期：

- `/login` 返回 NewAPI 页面，不触发 ALTCHA。
- `/register` 和 `/sign-up` 返回 302，跳到 `/auth-gate/verify`。
- 未验证调用注册接口返回 401。
- `/register?aff=xxx` 和 `/sign-up?aff=xxx` 完成验证后保留邀请参数。

## 配置项

- `ALTCHA_MAX_NUMBER`：PoW 搜索上限，默认 `100000`。
- `ALTCHA_COOKIE_TTL_SECONDS`：放行 Cookie 有效期，默认 `300` 秒。
- `ALTCHA_CHALLENGE_TTL_SECONDS`：挑战有效期，默认 `120` 秒。
- `ALTCHA_COOKIE_SECURE`：生产 HTTPS 环境应为 `true`。

## 回滚

停止 `newapi-auth-gateway` 和 `newapi-altcha-gate`，恢复 NewAPI 原来的
宿主机端口映射。不要删除 MySQL、Redis 或数据卷。
