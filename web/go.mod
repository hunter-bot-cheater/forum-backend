// 这个文件没有任何 Go 代码，只为给 Go 划一条模块边界。
//
// 原因：web/node_modules 里的第三方包可能自带 .go 文件（例如 flatted 的 Go 实现），
// 它们不在根模块的意图内，却会被 `go build ./...` 当成自己的包去编译，
// 表现为「Access is denied」或莫名其妙的编译失败。
// Go 遇到嵌套的 go.mod 就不再向下扫描，所以在这里放一个空模块即可。
module forum-web-nogo

go 1.26.0
