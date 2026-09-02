package main

import (
	"io"
	"log"
	"net"
)

func main() {
	listener, err := net.Listen("tcp", "0.0.0.0:7373")
	if err != nil {
		log.Fatal(err)
	}

	for {
		client, err := listener.Accept()
		if err != nil {
			log.Printf("accept connection: %v", err)
			continue
		}
		go forward(client)
	}
}

func forward(client net.Conn) {
	defer client.Close()

	server, err := net.Dial("tcp", "127.0.0.1:7374")
	if err != nil {
		log.Printf("connect to Roborev: %v", err)
		return
	}
	defer server.Close()

	done := make(chan struct{}, 2)
	go copyConnection(server, client, done)
	go copyConnection(client, server, done)
	<-done
}

func copyConnection(dst, src net.Conn, done chan<- struct{}) {
	_, _ = io.Copy(dst, src)
	if tcp, ok := dst.(*net.TCPConn); ok {
		_ = tcp.CloseWrite()
	}
	done <- struct{}{}
}
