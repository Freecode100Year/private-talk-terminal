package chat

// refusalZh translates the errors Connect returns, for the bilingual
// message shown to a refused client.
var refusalZh = map[error]string{
	ErrBanned:       "这个地址暂时被封禁了",
	ErrFull:         "服务器已满，请稍后再试",
	ErrTooManyConns: "你的地址连接数太多了",
	ErrConnRate:     "连接太频繁，请等一分钟",
}

// RefusalText is the bilingual line shown when Connect refuses a client.
func RefusalText(err error) string {
	if zh, ok := refusalZh[err]; ok {
		return "抱歉：" + zh + " · Sorry: " + err.Error()
	}
	return "抱歉 Sorry: " + err.Error()
}
