# Builds WaveAlign with Go module mirrors that work in mainland China.
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GOSUMDB = "sum.golang.google.cn"
go tool mygo build @args
