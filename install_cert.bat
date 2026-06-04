@echo off
chcp 65001 >nul
echo ╔══════════════════════════════════════════════╗
echo ║     ChessMaster CA 证书安装脚本               ║
echo ╚══════════════════════════════════════════════╝
echo.

if not exist "ca.crt" (
    echo [错误] 未找到 ca.crt 文件
    echo 请先运行 chessmaster.exe 生成证书，再运行本脚本
    pause
    exit /b 1
)

echo 正在将 ca.crt 安装到「受信任的根证书颁发机构」...
echo （需要管理员权限，会弹出 UAC 确认）
echo.

certutil -addstore -f "ROOT" "ca.crt"

if %errorlevel% == 0 (
    echo.
    echo ✓ 证书安装成功！
    echo   现在可以正常抓取 HTTPS 流量了。
) else (
    echo.
    echo [错误] 证书安装失败，请以管理员身份运行此脚本
    echo 右键点击 install_cert.bat → 以管理员身份运行
)

echo.
pause
