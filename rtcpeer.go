package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/oggreader"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
)

const (
	audioSource     = "resources/sources/audio.ogg"
	videoSource     = "resources/sources/video.mp4"
	outputPath      = "resources/results/"
	oggPageDuration = time.Millisecond * 20
)

var (
	audioCodec = webrtc.RTPCodecCapability{
		MimeType:     webrtc.MimeTypeOpus,
		ClockRate:    48000,
	}
	videoCodec = webrtc.RTPCodecCapability{
		MimeType:     webrtc.MimeTypeH264,
		ClockRate:    90000,
	}
)


var rtcConf = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			// Don't need STUN for this
			URLs: []string{},
		},
	},
}

type ConnectionState int

const (
	Standby ConnectionState = iota
	Ringing
	Answering
	InCall
	Closed
)

type ConnectionMode int

const (
	TextConnection ConnectionMode = iota
	VoiceConnectionSimplex
	VoiceConnectionDuplex
	VideoConnectionSimplex
)

type SignalAction int

const (
	Offer SignalAction = iota
	Answer
	Refuse
)

type audioSender struct {
	track *webrtc.TrackLocalStaticSample
	rtp   *webrtc.RTPSender
	ogg   *oggreader.OggReader
}

type audioReceiver struct {
	out   string
	track *webrtc.TrackRemote
	rtp   *webrtc.RTPReceiver
}

type Connection struct {
	local             *RTCPeer
	peer              *webrtc.PeerConnection
	remoteAddr        string
	isInitiator       bool
	mode              ConnectionMode
	state             ConnectionState
	candidatesMutex   sync.Mutex
	pendingCandidates []*webrtc.ICECandidate
	dataChan          *webrtc.DataChannel
	audioSndr         *audioSender
	audioRcvr         *audioReceiver
}

type RTCPeer struct {
	listenAddr  string
	Connections map[string]*Connection
}

type SignalSDP struct {
	SDP    webrtc.SessionDescription
	Action SignalAction
	Mode   ConnectionMode
	Origin string
}

type SignalCandidate struct {
	Candidate string
	Origin    string
}

func NewRTCPeer(listen string) *RTCPeer {
	peer := &RTCPeer{
		Connections: make(map[string]*Connection),
		listenAddr:  listen,
	}

	http.HandleFunc("/candidate", peer.httpHandleCandidate)
	http.HandleFunc("/sdp", peer.httpHandleSDP)

	return peer
}

func newConnection(
	local *RTCPeer,
	remote string,
	mode ConnectionMode,
) (*Connection, error) {
	conn := &Connection{
		local:             local,
		state:             Standby,
		mode:              mode,
		pendingCandidates: make([]*webrtc.ICECandidate, 0),
	}

	var err error
	conn.peer, err = webrtc.NewPeerConnection(rtcConf)
	if err != nil {
		return nil, err
	}

	conn.peer.OnConnectionStateChange(conn.handleConnectionStateChange)
	conn.peer.OnICECandidate(conn.handleICECandidate)
	conn.peer.OnDataChannel(func(d *webrtc.DataChannel) {
		conn.dataChan = d
		conn.dataChan.OnOpen(conn.handleDataChanOpen)
		conn.dataChan.OnMessage(conn.handleDataChanMsg)
		conn.dataChan.OnClose(conn.handleDataChanClose)
	})

	return conn, nil
}

func (conn *Connection) signalCandidate(c *webrtc.ICECandidate) error {
	signal := SignalCandidate{
		Candidate: c.ToJSON().Candidate,
		Origin:    conn.local.listenAddr,
	}
	payload, err := json.Marshal(&signal)
	resp, err := http.Post(fmt.Sprintf("http://%s/candidate", conn.remoteAddr),
		"application/json; charset=utf-8", bytes.NewReader(payload))
	if err != nil {
		return err
	}

	if err := resp.Body.Close(); err != nil {
		return err
	}

	return nil
}

func (conn *Connection) handleICECandidate(c *webrtc.ICECandidate) {
	if c == nil {
		return
	}

	conn.candidatesMutex.Lock()
	defer conn.candidatesMutex.Unlock()

	desc := conn.peer.RemoteDescription()
	if desc == nil {
		conn.pendingCandidates = append(conn.pendingCandidates, c)
	} else if err := conn.signalCandidate(c); err != nil {
		panic(err)
	}
}

func (peer *RTCPeer) httpHandleCandidate(w http.ResponseWriter, r *http.Request) {
	var signal SignalCandidate
	if err := json.NewDecoder(r.Body).Decode(&signal); err != nil {
		log.Println("couldn't parse candidate: ", err)
		return
	}
	conn, ok := peer.Connections[signal.Origin]
	if !ok {
		log.Println(
			"got a candidate from",
			signal.Origin,
			"but wasn't expecting one",
		)
		return
	}
	err := conn.peer.AddICECandidate(webrtc.ICECandidateInit{
		Candidate: signal.Candidate,
	})
	if err != nil {
		log.Println("couldn't initialize candidate: ", err)
	}
}

func (peer *RTCPeer) httpHandleSDP(w http.ResponseWriter, r *http.Request) {
	var signal SignalSDP
	if err := json.NewDecoder(r.Body).Decode(&signal); err != nil {
		log.Println("couldn't parse signal message from json: ", err)
		return
	}

	var err error
	conn, ok := peer.Connections[signal.Origin]
	if !ok {
		conn, err = newConnection(peer, signal.Origin, signal.Mode)
		if err != nil {
			log.Println("couldn't create new connection:", err)
			return
		}
		peer.Connections[signal.Origin] = conn
	}

	switch signal.Action {
	case Offer:
		if conn.state != Standby {
			log.Println("answering incoming call from", signal.Origin,
				"but we are busy")
			return
		}
		conn.state = Answering
		conn.remoteAddr = signal.Origin
		log.Println("incoming call from ", conn.remoteAddr)
	case Answer:
		if conn.state != Ringing {
			log.Println("answer from", signal.Origin,
				"but we weren't calling")
			return
		}
		log.Println("answer from ", conn.remoteAddr)
	case Refuse:
		if conn.state != Ringing {
			log.Println("refusal from", signal.Origin,
				"but we weren't calling")
			return
		}
		log.Println(signal.Origin, "appears to be busy")
		conn.state = Standby
		return
	default:
		log.Println(signal.Origin,
			"appears to be having problems communicating")
		return
	}

	switch conn.mode {
	case VoiceConnectionSimplex:
		if signal.Action == Offer {
			conn.getAudio()
		}
	case VoiceConnectionDuplex:
		conn.getAudio()
	}

	if err := conn.peer.SetRemoteDescription(signal.SDP); err != nil {
		log.Println("couldn't set remote sdp: ", err)
		answer := SignalSDP{Action: Refuse, Origin: peer.listenAddr}
		payload, err := json.Marshal(answer)
		if err != nil {
			log.Println("unable to marshal sdp answer: ", err)
			return
		}
		resp, err := http.Post(
			fmt.Sprintf("http://%s/sdp", signal.Origin),
			"application/json; charset=utf-8",
			bytes.NewReader(payload),
		)
		if err != nil {
			log.Println("unable to send sdp answer: ", err)
			return
		} else if err := resp.Body.Close(); err != nil {
			log.Println("http error on close: ", err)
			return
		}
		return
	}

	// We are answering the call, so we need to create an SDP answer
	if conn.state == Answering {
		var err error
		answer := SignalSDP{Action: Answer, Origin: peer.listenAddr}
		answer.SDP, err = conn.peer.CreateAnswer(nil)
		if err != nil {
			log.Println("unable to create sdp answer: ", err)
			return
		}

		payload, err := json.Marshal(answer)
		if err != nil {
			log.Println("unable to marshal sdp answer: ", err)
			return
		}
		resp, err := http.Post(
			fmt.Sprintf("http://%s/sdp", conn.remoteAddr),
			"application/json; charset=utf-8",
			bytes.NewReader(payload),
		)
		if err != nil {
			log.Println("unable to send sdp answer: ", err)
			return
		} else if err := resp.Body.Close(); err != nil {
			log.Println("http error on close: ", err)
			return
		}

		err = conn.peer.SetLocalDescription(answer.SDP)
		if err != nil {
			log.Println("unable to set local sdp", err)
			return
		}
	}

	conn.candidatesMutex.Lock()
	defer conn.candidatesMutex.Unlock()

	for _, c := range conn.pendingCandidates {
		if err := conn.signalCandidate(c); err != nil {
			log.Println("unable to signal remote conn: ", err)
			return
		}
	}
	conn.state = InCall
}

func (conn *Connection) handleConnectionStateChange(s webrtc.PeerConnectionState) {
	log.Println("peer connection state has changed: ", s.String())

	switch s {
	case webrtc.PeerConnectionStateConnected:
		conn.state = InCall
		switch conn.mode {
		case VoiceConnectionSimplex:
			if conn.isInitiator {
				go conn.sendAudio()
			}
		case VoiceConnectionDuplex:
			go conn.sendAudio()
		}
	case webrtc.PeerConnectionStateFailed:
		fallthrough
	case webrtc.PeerConnectionStateDisconnected:
		conn.Close()
		fallthrough
	case webrtc.PeerConnectionStateClosed:
		conn.state = Closed
	}
}

func (conn *Connection) handleDataChanOpen() {
	log.Printf(
		"data channel %s@%s — %d open\n",
		conn.dataChan.Label(),
		conn,
		conn.dataChan.ID(),
	)
}

func (conn *Connection) handleDataChanClose() {
	log.Printf(
		"data channel %s@%s — %d closed\n",
		conn.dataChan.Label(),
		conn,
		conn.dataChan.ID(),
	)
	conn.dataChan = nil
	if err := conn.Close(); err != nil {
		log.Println("something happened while attempting to close connection:", err)
	}
}

func (conn *Connection) handleDataChanMsg(msg webrtc.DataChannelMessage) {
	log.Printf(
		"channel %s@%s: %s\n",
		conn.dataChan.Label(),
		conn,
		string(msg.Data),
	)
}

func (conn *Connection) saveToDisk(i media.Writer, track *webrtc.TrackRemote) {
	defer func() {
		if err := i.Close(); err != nil {
			log.Println("error closing file:", err)
		}
	}()

	for conn.state == InCall {
		packet, _, err := track.ReadRTP()
		if err != nil {
			log.Println("error reading rtp stream:", err)
			conn.Close()
			return
		}
		if err := i.WriteRTP(packet); err != nil {
			log.Println("error writing to disk:", err)
			conn.Close()
			return
		}
	}
}

func (conn *Connection) getAudio() error {
	_, err := conn.peer.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio)
	if err != nil {
		return err
	}
	fname := fmt.Sprintf("%s/%s.opus", outputPath, conn)
	file, err := oggwriter.New(fname, 48000, 2)
	if err != nil {
		return err
	}

	conn.peer.OnTrack(func(
		track *webrtc.TrackRemote,
		recvr *webrtc.RTPReceiver,
	) {
		// Send a PLI on an interval so that the publisher is pushing a keyframe
		// every rtcpPLIInterval
		go func() {
			ticker := time.NewTicker(time.Second * 3)
			for range ticker.C {
				if conn.state != InCall {
					return
				}
				err := conn.peer.WriteRTCP(
					[]rtcp.Packet{
						&rtcp.PictureLossIndication{
							MediaSSRC: uint32(track.SSRC()),
						},
					},
				)
				if err != nil {
					log.Println("RTCP error:", err)
				}
			}
		}()

		log.Println("writing track to", fname)
		codec := track.Codec()
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeOpus) {
			conn.saveToDisk(file, track)
		}
	})

	return err
}

func (conn *Connection) loadAudio(fname string) error {
	var err error
	conn.audioSndr = new(audioSender)
	conn.audioSndr.track, err = webrtc.NewTrackLocalStaticSample(
		audioCodec,
		"audio",
		conn.String(),
	)
	if err != nil {
		return err
	}
	conn.audioSndr.rtp, err = conn.peer.AddTrack(conn.audioSndr.track)
	if err != nil {
		return err
	}

	file, err := os.Open(fname)
	if err != nil {
		return err
	}
	conn.audioSndr.ogg, _, err = oggreader.NewWith(file)

	return err
}

func (conn *Connection) sendAudio() {
	var lastGranule uint64
	ticker := time.NewTicker(oggPageDuration)
	log.Println("sending audio")
	for ; conn.state == InCall; <-ticker.C {
		pageData, pageHeader, err := conn.audioSndr.ogg.ParseNextPage()
		if err == io.EOF {
			log.Println("end of audio")
			conn.Close()
			return
		} else if err != nil {
			log.Println("error reading audio pages:", err)
			conn.Close()
			return
		}

		sampleCount := float64(pageHeader.GranulePosition - lastGranule)
		lastGranule = pageHeader.GranulePosition
		sampleDuration :=
			time.Duration((sampleCount/float64(audioCodec.ClockRate))*1000) *
			time.Millisecond
		err = conn.audioSndr.track.WriteSample(media.Sample{
			Data:     pageData,
			Duration: sampleDuration,
		})
		if err != nil {
			log.Println("error writing samples:", err)
			conn.Close()
			return
		}
	}
}

func (peer *RTCPeer) Ring(remote string, mode ConnectionMode) *Connection {
	if _, ok := peer.Connections[remote]; ok {
		log.Println("you are already connected to", remote)
		return nil
	}

	conn, err := newConnection(peer, remote, mode)
	if err != nil {
		log.Println("couldn't create new connection:", err)
		return nil
	}
	conn.isInitiator = true

	var offer SignalSDP
	var payload []byte
	var resp *http.Response
	// A data channel will always be created
	conn.dataChan, err = conn.peer.CreateDataChannel("data", nil)
	peer.Connections[remote] = conn
	if err != nil {
		log.Println("unable to create data channel: ", err)
		goto fail
	}
	conn.dataChan.OnOpen(conn.handleDataChanOpen)
	conn.dataChan.OnMessage(conn.handleDataChanMsg)
	conn.dataChan.OnClose(conn.handleDataChanClose)

	switch mode {
	case VoiceConnectionSimplex:
		fallthrough
	case VoiceConnectionDuplex:
		if err := conn.loadAudio(audioSource); err != nil {
			log.Println(
				"can't start voice call, problem loading audio file:",
				err,
			)
			goto fail
		}
	}

	offer = SignalSDP{Action: Offer, Mode: mode, Origin: peer.listenAddr}
	offer.SDP, err = conn.peer.CreateOffer(nil)
	if err != nil {
		log.Println("unable to create offer: ", err)
		goto fail
	}
	if err = conn.peer.SetLocalDescription(offer.SDP); err != nil {
		log.Println("unable to set local description: ", err)
		goto fail
	}
	payload, err = json.Marshal(&offer)
	if err != nil {
		log.Println("unable to marshal offer into json: ", err)
		goto fail
	}
	conn.remoteAddr = remote
	conn.state = Ringing
	log.Println("dialing", remote)
	resp, err = http.Post(
		fmt.Sprintf("http://%s/sdp", remote),
		"application/json; charset=utf-8",
		bytes.NewReader(payload),
	)
	if err != nil {
		log.Println("unable to dial", remote, "conn: ", err)
		goto fail
	}
	if err := resp.Body.Close(); err != nil {
		log.Println("unable to close response: ", err)
		goto fail
	}
	return conn
fail:
	conn.Close()
	return nil
}

func (conn *Connection) SendMsg(msg string) {
	if conn.state != InCall {
		log.Println("but there was nobody listening...")
		return
	}
	if err := conn.dataChan.SendText(msg); err != nil {
		log.Println("couldn't send message to ", conn, ": ", err)
	}
}

func (peer *RTCPeer) SendMsgToAll(msg string) {
	for _, conn := range peer.Connections {
		conn.SendMsg(msg)
	}
}

func (peer *RTCPeer) HangUp(remote string) {
	conn, ok := peer.Connections[remote]
	if !ok {
		log.Println("not connected to", remote)
		return
	}
	err := conn.Close()
	if err != nil {
		log.Println("unable to close peer connection: ", err)
	}
}

func (conn *Connection) Close() error {
	if conn.state == Closed {
		return nil
	}
	conn.state = Closed
	if conn.dataChan != nil {
		conn.dataChan.Close()
	}
	err := conn.peer.Close()
	log.Printf("connection to %s closed\n", conn)
	delete(conn.local.Connections, conn.remoteAddr)
	return err
}

func (conn *Connection) String() string {
	return conn.remoteAddr
}

func (peer *RTCPeer) CloseAll() {
	for k, conn := range peer.Connections {
		if err := conn.Close(); err != nil {
			log.Println("unable to close peer", k, "connection: ", err)
		}
	}
}

func (peer *RTCPeer) Listen() {
	log.Println("listening at", peer.listenAddr)
	log.Fatal(http.ListenAndServe(peer.listenAddr, nil))
}
