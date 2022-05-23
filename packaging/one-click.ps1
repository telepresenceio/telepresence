Invoke-WebRequest https://app.getambassador.io/download/tel2/windows/amd64/latest/telepresence.zip -OutFile telepresence.zip
Expand-Archive -Path telepresence.zip -DestinationPath telepresenceInstaller/telepresence
Remove-Item 'telepresence.zip'
cd telepresenceInstaller/telepresence
powershell.exe -ExecutionPolicy bypass -c " . '.\install-telepresence.ps1';"
cd ../..
Remove-Item telepresenceInstaller -Recurse -Confirm:$false -Force