@echo off
chcp 65001 >nul
title ChessMaster 抓包工具
echo 正在启动 ChessMaster 抓包工具...
echo.

cd /d "%~dp0"
chessmaster.exe -proxy :8899 -logdir logs

echo.
echo 程序已退出，按任意键关闭...
pause >nul
