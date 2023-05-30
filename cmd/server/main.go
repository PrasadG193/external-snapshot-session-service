package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"os"

	cbtv1alpha1 "github.com/PrasadG193/snapshot-session-access/pkg/api/cbt/v1alpha1"
	ssa "github.com/PrasadG193/snapshot-session-access/pkg/controller"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	pgrpc "github.com/PrasadG193/external-snapshot-session-service/pkg/grpc"
)

const (
	PROTOCOL = "unix"
	SOCKET   = "/csi/csi.sock"

	podNamespaceEnvKey = "POD_NAMESPACE"
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
	pgrpc.RegisterSnapshotMetadataServer(s, newServer())
	log.Println("SERVER STARTED!")
	if err := s.Serve(listener); err != nil {
		log.Fatal(err)
	}
}

func newServer() *Server {
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("could not init in cluster config %v", err)
	}
	dynCli, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("failed to create dynamic client %v", err)
	}
	return &Server{
		kubeCli: dynCli,
	}
}

type Server struct {
	client pgrpc.SnapshotMetadataClient
	pgrpc.UnimplementedSnapshotMetadataServer
	kubeCli dynamic.Interface
}

func (s *Server) convertParams(ctx context.Context, req *pgrpc.GetDeltaRequest) (*pgrpc.GetDeltaRequest, error) {
	// 1. Fetch CSISnapshotSessionData resource for the given token
	log.Println("Fetching CSISnapshotSessionData resource for the token", req.SessionToken)
	ssd, err := s.findSnapshotSessionData(ctx, req.SessionToken)
	if err != nil {
		return nil, err
	}
	// 2. Validate the session parameters
	return s.validateAndTranslateParams(ctx, req, ssd)
}

func (s *Server) validateAndTranslateParams(ctx context.Context, req *pgrpc.GetDeltaRequest, ssd *cbtv1alpha1.CSISnapshotSessionData) (*pgrpc.GetDeltaRequest, error) {
	newReq := pgrpc.GetDeltaRequest{}
	// The session token is valid for basesnapshot
	for _, snap := range ssd.Spec.Snapshots {
		if snap.Name == req.BaseSnapshot {
			newReq.BaseSnapshot = snap.SnapshotHandle
			newReq.VolumeId = snap.VolumeHandle
			continue
		}
		if snap.Name == req.TargetSnapshot {
			newReq.TargetSnapshot = snap.SnapshotHandle
			continue
		}
	}
	// Return error if the SnapSessionData doesn't have entry for the snaps in request
	if newReq.BaseSnapshot == "" || newReq.TargetSnapshot == "" {
		return nil, fmt.Errorf("Invalid token for specified volumesnapshots in the request")
	}
	newReq.StartingByteOffset = req.StartingByteOffset
	newReq.MaxResults = req.MaxResults
	return &newReq, nil
}

func (s *Server) findSnapshotSessionData(ctx context.Context, token string) (*cbtv1alpha1.CSISnapshotSessionData, error) {
	gvr := schema.GroupVersionResource{Group: "cbt.storage.k8s.io", Version: "v1alpha1", Resource: "csisnapshotsessiondata"}
	us, err := s.kubeCli.Resource(gvr).Namespace(os.Getenv(podNamespaceEnvKey)).Get(ctx, ssa.SnapSessionDataNameWithToken(token), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("Not found csisnapshotsessiondata resource for the token, %v", err)
	}
	log.Printf("Found CSISnapshotSessionData: %s for the token  %s\n", us.GetName(), token)
	var ssd cbtv1alpha1.CSISnapshotSessionData
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(us.UnstructuredContent(), &ssd)
	if err != nil {
		return nil, err
	}
	return &ssd, nil
}

func (s *Server) GetDelta(req *pgrpc.GetDeltaRequest, cbtClientStream pgrpc.SnapshotMetadata_GetDeltaServer) error {
	log.Println("Received request::", req.String())

	s.initCSIGRPCClient()
	ctx := context.Background()
	spReq, err := s.convertParams(ctx, req)
	if err != nil {
		return err
	}

	// Call CSI Driver's GetDelta gRPC
	csiStream, err := s.client.GetDelta(ctx, spReq)
	if err != nil {
		return err
	}
	done := make(chan bool)
	go func() {
		for {
			resp, err := csiStream.Recv()
			if err == io.EOF {
				done <- true //means stream is finished
				return
			}
			if err != nil {
				log.Printf(fmt.Sprintf("cannot receive %v", err))
				return
			}
			log.Println("Received response from csi driver, proxying to client")
			if err := cbtClientStream.Send(resp); err != nil {
				log.Printf(fmt.Sprintf("cannot send %v", err))
				return
			}
		}
	}()
	<-done //we will wait until all response is received
	return nil
}

func (s *Server) initCSIGRPCClient() {
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, PROTOCOL, addr)
	}
	options := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(dialer),
	}
	conn, err := grpc.Dial(SOCKET, options...)
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	s.client = pgrpc.NewSnapshotMetadataClient(conn)

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
