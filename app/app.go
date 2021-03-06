package app

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"

	"github.com/elazarl/go-bindata-assetfs"
	"github.com/gorilla/websocket"
	"github.com/kr/pty"
)

type App struct {
	Address     string
	Port        string
	PermitWrite bool
	Command     []string
}

func New(address string, port string, permitWrite bool, command []string) *App {
	return &App{
		Address:     address,
		Port:        port,
		PermitWrite: permitWrite,
		Command:     command,
	}
}

func (app *App) Run() error {
	http.Handle("/",
		http.FileServer(
			&assetfs.AssetFS{Asset: Asset, AssetDir: AssetDir, Prefix: "bindata"},
		),
	)
	http.HandleFunc("/ws", app.generateHandler())

	url := app.Address + ":" + app.Port
	log.Printf("Sever is running at %s, command: %s", url, strings.Join(app.Command, " "))
	err := http.ListenAndServe(url, nil)
	if err != nil {
		return err
	}

	return nil
}

func (app *App) generateHandler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("New client connected: %s", r.RemoteAddr)

		upgrader := websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			Subprotocols:    []string{"gotty"},
		}

		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Print("Failed to upgrade connection")
			return
		}

		cmd := exec.Command(app.Command[0], app.Command[1:]...)
		fio, err := pty.Start(cmd)
		if err != nil {
			log.Print("Failed to execute command")
			return
		}

		exit := make(chan bool, 2)

		go func() {
			defer func() { exit <- true }()

			buf := make([]byte, 1024)
			leftOver := 0
			for {
				size, err := fio.Read(buf[leftOver:])
				size += leftOver

				if err != nil {
					log.Printf("command exited for: %s", r.RemoteAddr)
					return
				}

				writer, err := conn.NextWriter(websocket.TextMessage)
				if err != nil {
					return
				}

				// UTF-8 Boundary check
				for leftOver = 0; leftOver < utf8.UTFMax; leftOver++ {
					re, _ := utf8.DecodeLastRune(
						buf[:size-leftOver],
					)

					if re != utf8.RuneError {
						break
					}
					// Invalid UTF rune
				}

				if leftOver == utf8.UTFMax-1 {
					re, _ := utf8.DecodeLastRune(buf[:size-leftOver])
					if re == utf8.RuneError {
						log.Fatal("UTF8 Boundary error.")
					}
				}

				writer.Write(buf[:size-leftOver])
				writer.Close()

				for i := 0; i < leftOver; i++ {
					buf[i] = buf[size-leftOver+i]
				}
			}
		}()

		go func() {
			defer func() { exit <- true }()

			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}

				switch data[0] {
				case '0':
					if !app.PermitWrite {
						break
					}

					_, err := fio.Write(data[1:])
					if err != nil {
						return
					}

				case '1':
					var remoteCmd command
					err = json.Unmarshal(data[1:], &remoteCmd)
					if err != nil {
						log.Print("Malformed remote command")
						return
					}

					switch remoteCmd.Name {
					case "resize_terminal":

						rows := remoteCmd.Arguments["rows"]
						switch rows.(type) {
						case float64:
						default:
							log.Print("Malformed remote command")
							return
						}

						cols := remoteCmd.Arguments["columns"]
						switch cols.(type) {
						case float64:
						default:
							log.Print("Malformed remote command")
							return
						}

						window := struct {
							row uint16
							col uint16
							x   uint16
							y   uint16
						}{
							uint16(rows.(float64)),
							uint16(cols.(float64)),
							0,
							0,
						}
						syscall.Syscall(
							syscall.SYS_IOCTL,
							fio.Fd(),
							syscall.TIOCSWINSZ,
							uintptr(unsafe.Pointer(&window)),
						)
					}

				default:
					log.Print("Unknown message type")
					return
				}
			}
		}()

		go func() {
			<-exit
			fio.Write([]byte{4})
			fio.Close()
			conn.Close()
			log.Printf("Connection closed: %s", r.RemoteAddr)
		}()
	}
}

type command struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}
