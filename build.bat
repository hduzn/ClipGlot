@echo off
cd /d %~dp0
wails build -webview2 embed -ldflags "-s -w -H windowsgui"
if errorlevel 1 exit /b 1

rem Version comes from info.productVersion in wails.json (ASCII only, cmd reads bat as ANSI/GBK)
set VER=
for /f %%v in ('powershell -NoProfile -Command "(Get-Content wails.json -Raw | ConvertFrom-Json).info.productVersion"') do set VER=%%v
if "%VER%"=="" set VER=0.0.0

if not exist release mkdir release
copy /y build\bin\ClipGlot.exe "release\ClipGlot_v%VER%.exe" >nul
if errorlevel 1 (
	echo Copy FAILED: close the running ClipGlot and run build.bat again.
	exit /b 1
)
echo Build OK: release\ClipGlot_v%VER%.exe
