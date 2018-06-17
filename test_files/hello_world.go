package main  // have the main here

import "air" // import top package we are developing

func main() {
	air.GET("/", func(req *air.Request, res *air.Response) error {
		return res.String("Hello, 世界")
	})
	air.Serve()
}