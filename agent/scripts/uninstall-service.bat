@echo off
set NSSM=nssm.exe
set SERVICE_NAME=opsview-agent

echo Stopping %SERVICE_NAME%...
net stop %SERVICE_NAME% 2>nul
echo Removing %SERVICE_NAME%...
%NSSM% remove %SERVICE_NAME% confirm
echo Done.
