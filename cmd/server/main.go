package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"

	pgrpc "github.com/PrasadG193/external-snapshot-session-service/pkg/grpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/reflection"
)

func main() {
	listener, err := net.Listen("tcp", ":9000")
	if err != nil {
		panic(err)
	}

	tlsCredentials, err := loadTLSCredentials()
	if err != nil {
		log.Fatal("cannot load TLS credentials: ", err)
	}
	s := grpc.NewServer(
		grpc.Creds(tlsCredentials),
	)
	reflection.Register(s)
	pgrpc.RegisterSnapshotMetadataServer(s, &Server{})
	fmt.Println("SERVER STARTED!")
	if err := s.Serve(listener); err != nil {
		log.Fatal(err)
	}
}

type Server struct {
	pgrpc.UnimplementedSnapshotMetadataServer
}

func (s *Server) GetDelta(req *pgrpc.GetDeltaRequest, stream pgrpc.SnapshotMetadata_GetDeltaServer) error {
	fmt.Println("Received request::", req.String())
	resp := pgrpc.GetDeltaResponse{
		BlockMetadataType: pgrpc.BlockMetadataType_FIXED_LENGTH,
		VolumeSizeBytes:   1024 * 1024 * 1024,
		BlockMetadata: []*pgrpc.BlockMetadata{
			&pgrpc.BlockMetadata{
				ByteOffset: 1,
				SizeBytes:  1024 * 1024,
			},
		},
	}
	for i := 1; i <= 10; i++ {
		resp.BlockMetadata[0].ByteOffset = uint64(i)
		if err := stream.Send(&resp); err != nil {
			return err
		}
	}
	return nil
}

func loadTLSCredentials() (credentials.TransportCredentials, error) {
	// Load server's certificate and private key
	cert := os.Getenv("CBT_SERVER_CERT")
	key := os.Getenv("CBT_SERVER_KEY")
	serverCert, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}

	// Create the credentials and return it
	config := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.NoClientCert,
	}

	return credentials.NewTLS(config), nil
}
