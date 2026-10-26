# A loopback listener standing in for the SuiteWard service on the host: answers every connection and logs the peer.
# usage: pwsh -File listen.ps1 [-Port 18081] [-Address 127.0.0.1]
param([int]$Port = 18081, [string]$Address = '127.0.0.1')
$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Parse($Address), $Port)
$listener.Start()
Write-Host "listening on ${Address}:$Port"
while ($true) {
	$client = $listener.AcceptTcpClient()
	Write-Host ("{0:o} connection from {1}" -f (Get-Date), $client.Client.RemoteEndPoint)
	$body = [Text.Encoding]::ASCII.GetBytes("host-listener-$Port`n")
	$head = [Text.Encoding]::ASCII.GetBytes("HTTP/1.0 200 OK`r`nContent-Length: $($body.Length)`r`n`r`n")
	$stream = $client.GetStream()
	$stream.Write($head, 0, $head.Length); $stream.Write($body, 0, $body.Length)
	$client.Close()
}
