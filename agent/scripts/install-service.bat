@echo off
REM Install opsview-agent as a Windows service using NSSM
REM Download NSSM from https://nssm.cc/

set NSSM=nssm.exe
set SERVICE_NAME=opsview-agent
set AGENT_EXE=%~dp0\..\opsview-agent.exe

REM === REQUIRED: shared publisher secret — must equal the relay's ===
REM === RELAY_PUBLISHER_TOKEN. The relay rejects the agent without it. ===
REM Either fill it in below, or set RELAY_PUBLISHER_TOKEN in the environment
REM before running this script (generate one with: openssl rand -hex 32).
if "%RELAY_PUBLISHER_TOKEN%"=="" set RELAY_PUBLISHER_TOKEN=
if "%RELAY_PUBLISHER_TOKEN%"=="" (
  echo ERROR: RELAY_PUBLISHER_TOKEN is not set.
  echo   Set it at the top of this script, or run:  set RELAY_PUBLISHER_TOKEN=^<token^>
  exit /b 1
)

echo Installing %SERVICE_NAME%...
%NSSM% install %SERVICE_NAME% "%AGENT_EXE%"
%NSSM% set %SERVICE_NAME% AppEnvironmentExtra RELAY_PUBLISHER_TOKEN=%RELAY_PUBLISHER_TOKEN%
%NSSM% set %SERVICE_NAME% DisplayName "OpsView Agent"
%NSSM% set %SERVICE_NAME% Description "OpsView screen capture and streaming agent"
%NSSM% set %SERVICE_NAME% Start SERVICE_AUTO_START
%NSSM% set %SERVICE_NAME% AppStdout %~dp0\..\logs\agent.log
%NSSM% set %SERVICE_NAME% AppStderr %~dp0\..\logs\agent.log
%NSSM% set %SERVICE_NAME% AppRotateFiles 1
%NSSM% set %SERVICE_NAME% AppRotateBytes 10485760

REM Recovery: restart on failure (1s, 5s, 30s delays)
sc failure %SERVICE_NAME% reset=86400 actions=restart/1000/restart/5000/restart/30000

echo Done. Start with: net start %SERVICE_NAME%
