$current_directory = (pwd).path
echo $current_directory
Start-Process msiexec -Wait -verb runAs -Args "/i $current_directory\winfsp.msi /passive /qn /L*V winfsp-install.log"
Start-Process msiexec -Wait -verb runAs -Args "/i $current_directory\sshfs-win.msi /passive /qn /L*V sshfs-win-install.log"
[Environment]::SetEnvironmentVariable("Path", "C:\\;C:\\Program Files\\SSHFS-Win\\bin;$ENV:Path", "Machine")
