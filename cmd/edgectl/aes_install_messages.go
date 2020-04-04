package main


// First message when beginning the AES Installation process
func (i *Installer) BeginAESInstallMessage()  {
	i.show.Println("========================================================================")
	i.show.Println("Beginning Ambassador Edge Stack Installation")
	i.show.Println()
}


// AES installation complete!
func (i *Installer) AESInstallCompleteMessage()  {
}


