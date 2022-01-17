package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/Yaroslav-95/wrtcion/gst"
)

func parseCommand(cmd string, rtcpeer *RTCPeer, tapp *tview.Application) {
	args := strings.SplitN(cmd, " ", 3)
	if args[0] == "/help" {
		log.Println("enter a command or send a message to all connected peers:")
		log.Println("commands available:")
		log.Println("/chat <address>")
		log.Println("/call <address>")
		log.Println("/end <address>")
		log.Println("/msg <address> <message>")
	} else if args[0] == "/chat" {
		if len(args) < 2 {
			log.Println("remote address missing")
			return
		}
		rtcpeer.Ring(args[1], TextConnection)
	} else if args[0] == "/call" {
		if len(args) < 2 {
			log.Println("remote address missing")
			return
		}
		rtcpeer.Ring(args[1], VoiceConnectionSimplex)
	} else if args[0] == "/end" {
		if len(args) < 2 {
			log.Println("specify whom")
			return
		}
		rtcpeer.HangUp(args[1])
	} else if args[0] == "/msg" {
		if len(args) < 2 {
			log.Println("specify whom")
			return
		}
		conn, ok := rtcpeer.Connections[args[1]]
		if !ok {
			log.Println("no such destination")
		}
		conn.SendMsg(cmd)
	} else if args[0] == "/exit" {
		rtcpeer.CloseAll()
		tapp.Stop()
	} else {
		rtcpeer.SendMsgToAll(cmd)
	}
}

func onInput(
	in *tview.InputField,
	rtcpeer *RTCPeer,
	tapp *tview.Application,
	key tcell.Key,
) {
	if key == tcell.KeyEnter {
		txt := in.GetText()
		log.Println("you:", txt)
		parseCommand(txt, rtcpeer, tapp)
		in.SetText("")
	} else if key == tcell.KeyEscape {
		in.SetText("")
	}
}

var listen = flag.String("l", "localhost:8001", "listen address")

func wrtcionMain() {
	flag.Parse()

	flog, err := os.OpenFile(
		fmt.Sprintf("/tmp/wrtcion-%s.log", *listen),
		os.O_CREATE|os.O_TRUNC|os.O_WRONLY,
		0755,
	)
	if err != nil {
		panic(err)
	}
	defer flog.Close()

	tapp := tview.NewApplication()
	msglog := tview.NewTextView()
	msglog.SetChangedFunc(func() {
		tapp.Draw()
	})
	wlog := io.MultiWriter(flog, msglog)
	log.SetOutput(wlog)
	rtcpeer := NewRTCPeer(*listen)
	msginput := tview.NewInputField().SetLabel("Message: ")
	msginput.SetDoneFunc(func(key tcell.Key) {
		onInput(msginput, rtcpeer, tapp, key)
	})
	grid := tview.NewGrid().
		SetRows(0, 1).
		SetColumns(0).
		SetBorders(true)
	grid.AddItem(msglog, 0, 0, 1, 1, 0, 0, false)
	grid.AddItem(msginput, 1, 0, 1, 1, 0, 0, true)
	go rtcpeer.Listen()
	defer rtcpeer.CloseAll()
	if err := tapp.SetRoot(grid, true).Run(); err != nil {
		panic(err)
	}
	os.Exit(0)
}

func init() {
	// We are using Gstreamer's autovideosink/autoaudiosink element to play
	// received media. This element, along with some others, sometimes require
	// that the process' main thread is used
	runtime.LockOSThread()
}

func main() {
	// Actual main loop
	go wrtcionMain()
	// Gstreamer's GMainLoop
	gst.StartMainLoop()
}
