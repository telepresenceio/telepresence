param(
    [Parameter(Mandatory)][string]$InputFile,
    [Parameter(Mandatory)][string]$OutputFile
)

Add-Type -AssemblyName System.Windows.Forms

$rtb = New-Object System.Windows.Forms.RichTextBox
$rtb.LoadFile($InputFile, [System.Windows.Forms.RichTextBoxStreamType]::PlainText)
$rtb.SaveFile($OutputFile, [System.Windows.Forms.RichTextBoxStreamType]::RichText)

Write-Host "Converted: $($InputFile) -> $OutputFile"
