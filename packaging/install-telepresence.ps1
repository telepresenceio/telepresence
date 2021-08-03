param
(
    $Path = "C:\telepresence"
)

echo "Installing telepresence to $Path"

Start-Process msiexec -Wait -verb runAs -Args "/i $current_directory\winfsp.msi /passive /qn /L*V winfsp-install.log"
Start-Process msiexec -Wait -verb runAs -Args "/i $current_directory\sshfs-win.msi /passive /qn /L*V sshfs-win-install.log"

if(!(test-path $Path))
{
    New-Item -ItemType Directory -Force -Path $Path
}

Copy-Item "telepresence.exe" -Destination "$Path" -Force
Copy-Item "wintun.dll" -Destination "$Path" -Force

# We update the PATH to include telepresence and its dependency, sshfs-win
[Environment]::SetEnvironmentVariable("Path", "C:\$Path;C:\\Program Files\\SSHFS-Win\\bin;$ENV:Path")
