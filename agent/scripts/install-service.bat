@echo off
REM Install opsview-agent as a Windows service using NSSM
REM Download NSSM from https://nssm.cc/

set NSSM=nssm.exe
set SERVICE_NAME=opsview-agent
set AGENT_EXE=%~dp0\..\opsview-agent.exe

echo Installing %SERVICE_NAME%...
%NSSM% install %SERVICE_NAME% "%AGENT_EXE%"
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
