package main

import (
	"context"
	"errors"
	"net"

	reuse "github.com/libp2p/go-reuseport"
	log "github.com/sirupsen/logrus"
)

// PFCPNode represents a PFCP endpoint of the UPF.
type PFCPNode struct {
	ctx context.Context
	// listening socket for new "PFCP connections"
	net.PacketConn
	// done is closed to signal shutdown complete
	done chan struct{}
	// channel for PFCPConn to signal exit by sending their remote address
	pConnDone chan string
	// map of existing connections
	pConns map[string]*PFCPConn
	// upf
	upf *upf
}

// NewPFCPNode create a new PFCPNode listening on local address.
func NewPFCPNode(ctx context.Context, upf *upf) *PFCPNode {
	lAddr := upf.n4SrcIP.String() + ":" + PFCPPort

	conn, err := reuse.ListenPacket("udp", lAddr)
	if err != nil {
		log.Fatalln("ListenUDP failed", err)
	}

	return &PFCPNode{
		ctx:        ctx,
		PacketConn: conn,
		done:       make(chan struct{}),
		pConnDone:  make(chan string, 100),
		pConns:     make(map[string]*PFCPConn),
		upf:        upf,
	}
}

func (node *PFCPNode) handleNewPeers() {
	lAddrStr := node.LocalAddr().String()
	log.Infoln("listening for new PFCP connections on", lAddrStr)

	for {
		buf := make([]byte, 1024)

		n, rAddr, err := node.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}

		rAddrStr := rAddr.String()

		_, ok := node.pConns[rAddrStr]
		if ok {
			log.Warnln("Drop packet for existing PFCPconn received from", rAddrStr)
			continue
		}

		log.Infoln(lAddrStr, "received new connection from", rAddrStr)

		// TODO: Logic to distinguish PFCPConn based on SEID
		p := NewPFCPConn(node.ctx, node.upf, node.pConnDone, lAddrStr, rAddrStr)
		node.pConns[rAddrStr] = p
		p.HandlePFCPMsg(buf[:n])

		go p.Serve()
	}
}

// Serve listens for the first packet from a new PFCP peer and creates PFCPConn.
func (node *PFCPNode) Serve() {
	go node.handleNewPeers()

	shutdown := false

	for !shutdown {
		select {
		case fseid := <-node.upf.reportNotifyChan:
			// TODO: Logic to distinguish PFCPConn based on SEID
			for _, pConn := range node.pConns {
				pConn.handleDigestReport(fseid)
				break
			}
		case rAddr := <-node.pConnDone:
			delete(node.pConns, rAddr)
			log.Infoln("Removed connection to", rAddr)
		case <-node.ctx.Done():
			shutdown = true
			log.Infoln("Entering node shutdown")

			err := node.Close()
			if err != nil {
				log.Errorln("Error closing PFCPNode Conn", err)
			}

			// Clear out the remaining pconn completions
			for len(node.pConns) > 0 {
				rAddr := <-node.pConnDone
				delete(node.pConns, rAddr)
				log.Infoln("Removed connection to", rAddr)
			}

			close(node.pConnDone)
			log.Infoln("Done waiting for PFCPConn completions")
		}
	}

	close(node.done)
}

// Done waits for Shutdown() to complete
func (node *PFCPNode) Done() {
	<-node.done
	log.Infoln("Shutdown complete")
	return
}