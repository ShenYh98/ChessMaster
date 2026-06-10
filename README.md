# ChessMaster

一款象棋辅助软件，支持微信小程序平台的 JJ 象棋，可通过 web 页面查看推荐走法，简单直观。

通过 HTTP/HTTPS 中间人代理拦截小程序对局协议，结合 Pikafish 引擎实时给出推荐着法，并在浏览器里同步显示棋盘与思考结果。

---

## 功能

- ✅ 自动开启 / 还原 Windows 系统代理，无需手动配置浏览器
- ✅ HTTPS 中间人解密，自动签发动态证书
- ✅ 解析微信小程序对局报文
- ✅ 调用 Pikafish 引擎实时计算最佳着法
- ✅ 内嵌 Web 棋盘可视化（`http://127.0.0.1:8900`），中文棋子 + 推荐箭头

---

## 快速上手

### Step 1. 首次运行 → 生成 CA 证书

双击 `chessmaster.exe`，会在当前目录自动生成：
- `ca.crt` — CA 根证书（公开）
- `ca.key` — CA 私钥（**严禁泄露**，泄露等同于本机 HTTPS 完全裸奔）

> 这一步只是让程序把证书生成出来。第一次启动时由于证书还未被系统信任，HTTPS 会失败，先 `Ctrl+C` 退出即可，下一步再装证书。

### Step 2. 安装 CA 证书到系统受信任根

**右键** `install_cert.bat` → **以管理员身份运行**。

脚本本质上是调用：
```powershell
certutil -addstore -f "ROOT" "ca.crt"
```
把 `ChessMaster MITM CA` 装进 Windows「受信任的根证书颁发机构」，之后微信小程序的 HTTPS 流量才能被解密。

✅ 看到 `证书 "ChessMaster MITM CA" 已添加到存储区` 即成功。

### Step 3. 启动主程序

再次双击 `chessmaster.exe`，会自动完成：
1. ✓ 加载 `ca.crt` / `ca.key`
2. ✓ 启动 Pikafish 引擎（`pikafish/pikafish-avx2.exe`）
3. ✓ 监听代理端口 `127.0.0.1:8899`
4. ✓ 启动 Web 可视化 `http://127.0.0.1:8900`
5. ✓ 修改 Windows 系统代理 → 指向 `127.0.0.1:8899`
6. ✓ 自动弹出独立的棋盘窗口（用 Edge / Chrome 的 `--app` 模式）

控制台输出形如：
```
✓ CA 证书: ca.crt
✓ 日志目录: logs\
✓ 引擎推荐: 已启用 (自动识别、思考3000ms、4线程)
✓ 代理监听: 127.0.0.1:8899
✓ Web 棋盘可视化: http://127.0.0.1:8900
✓ Windows 系统代理已自动开启 → 127.0.0.1:8899
```

### Step 4. 打开微信小程序对弈

在 PC 微信里打开 JJ 象棋，进入一局对局。
浏览器棋盘窗口会实时显示：
- 棋盘当前局面（自动按我方颜色翻转视角）
- 上一步着法（淡黄色高亮）
- 引擎推荐着法（绿色起点 + 红色终点 + 红色箭头）
- 行棋历史

### Step 5. 退出

在主控制台按 `Ctrl+C`，或直接点窗口右上角 ❌：
- 系统代理自动还原到启动前的状态
- Web 棋盘窗口自动关闭
- 引擎进程自动结束

---

## 命令行参数

直接双击使用默认值即可。需要自定义时在 PowerShell 里运行：

```powershell
.\chessmaster.exe -movetime 5000 -threads 8
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-proxy` | `:8899` | 代理监听地址 |
| `-web` | `:8900` | Web 可视化监听地址（设为空字符串可禁用） |
| `-engineexe` | `pikafish/pikafish-avx2.exe` | Pikafish 可执行文件路径 |
| `-movetime` | `3000` | 引擎单步思考毫秒数 |
| `-threads` | `4` | 引擎使用的 CPU 线程数 |
| `-myseat` | `-1` | 我方席位（0=黑 1=红 -1=自动识别） |
| `-advisor` | `true` | 是否启用引擎推荐 |
| `-logdir` | `logs` | 日志输出目录 |
| `-maxmb` | `20` | 单个日志文件大小上限（MB） |
| `-enginetest` | `false` | 仅测试引擎调用后退出，不启动代理 |

测试引擎是否就绪（不开代理、不动系统设置）：
```powershell
.\chessmaster.exe -enginetest
```

---

## 卸载

1. 关闭 `chessmaster.exe`（系统代理会自动还原）
2. 删除信任的 CA 证书：
   ```powershell
   certutil -delstore "ROOT" "ChessMaster MITM CA"
   ```
   或在 `运行 → certmgr.msc → 受信任的根证书颁发机构 → 证书` 里手动删除
3. 删除整个 ChessMaster 目录即可

## 项目结构（开发者）

```
cert/        ← CA 根证书生成 + 动态子证书签发（带缓存）
proxy/       ← HTTP / HTTPS / WebSocket 中间人代理
sysproxy/    ← Windows 系统代理读写（注册表 + WinINet 通知）
engine/      ← Pikafish UCI 协议封装 + 局面追踪 + 推荐 Advisor
webui/       ← 内嵌静态资源 + SSE 推送实时快照 + 浏览器自启动器
logger/      ← HTTP/WS 报文落盘 + HTTP 错误日志独立文件
console_windows.go  ← 注册 Win32 ConsoleCtrlHandler 处理 X 关闭
```

构建：
```powershell
go build -o chessmaster.exe .
```

> ⚠️ **免责声明**：本项目仅供个人学习象棋协议解析、HTTPS MITM 抓包、UCI 引擎集成等技术研究使用。任何在线对弈场景挂载使用都属于违反平台协议的行为，且**极易被反作弊系统检测**（系统代理、自签 CA、进程名等都是明显特征）。后果自负，作者不承担任何责任。