echo off
setlocal
mkdir .wintools
mkdir .gocache

if "%TELEPRESENCE_REGISTRY%" == "" (
    echo "Please define a %%TELEPRESENCE_REGISTRY%% environment variable. It must be a docker repo to which you have logged in via Docker Desktop"
    exit \b
)

if not exist .wintools\jq.exe (
    curl -L https://github.com/stedolan/jq/releases/download/jq-1.6/jq-win64.exe --output .\.wintools\jq.exe
)

docker build . -f Dockerfile.winbuild -t tel2-winbuild || exit \b

set pwd=%~dp0
set pwd=%pwd:~3%
set pwd=%pwd:\=/%

set drive=%~dp0
set drive=%drive:~0,1%

@REM It's really cool how there's no `backticks` in batch scripts so you have to use a for loop to set variables
@REM Note also that we use a temp file because otherwise the jq command would have to have even more weird escape sequences
docker-credential-desktop.exe list | .\.wintools\jq.exe -r ". as $r | keys[] | select($r[.] == \"%TELEPRESENCE_REGISTRY%\") | sub(\"https://(?^<h^>[^^/]*)/.*\"; \"\(.h)\")" > .wintools\docker-host
for /f "delims=" %%i in (.wintools\docker-host) do set TELEPRESENCE_REGISTRY_HOST=%%i
del .wintools\docker-host

for /f "delims=" %%i in ('echo %TELEPRESENCE_REGISTRY_HOST% ^| docker-credential-desktop.exe get ^| .wintools\jq.exe -r .Username') do set "TELEPRESENCE_REGISTRY_USERNAME=%%i"
for /f "delims=" %%i in ('echo %TELEPRESENCE_REGISTRY_HOST% ^| docker-credential-desktop.exe get ^| .wintools\jq.exe -r .Secret') do set "TELEPRESENCE_REGISTRY_PASSWORD=%%i"

docker run --rm ^
    -v /host_mnt/%drive%/%pwd%:/source ^
    -v //var/run/docker.sock:/var/run/docker.sock ^
    -w /source ^
    -e GOOS=windows ^
    -e GOCACHE=/source/.gocache ^
    -e GOARCH ^
    -e TELEPRESENCE_REGISTRY ^
    -e TELEPRESENCE_REGISTRY_USERNAME ^
    -e TELEPRESENCE_REGISTRY_PASSWORD ^
    -e TELEPRESENCE_VERSION ^
    -e KO_DOCKER_REPO ^
    tel2-winbuild ^
    make _login %*