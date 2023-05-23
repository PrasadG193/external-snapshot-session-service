package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	cbtv1alpha1 "github.com/PrasadG193/cbt-datapath/pkg/api/cbt/v1alpha1"
	pgrpc "github.com/PrasadG193/external-snapshot-session-service/pkg/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	baseVolumeSnapshot   = "vs-005"
	targetVolumeSnapshot = "vs-006"
	clientNamespace      = "cbt-test"
)

var gvr = schema.GroupVersionResource{Group: "cbt.storage.k8s.io", Version: "v1alpha1", Resource: "csisnapshotsessionaccesses"}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := NewSnapshotMetadata()
	sessionParams, err := client.setupSession(ctx, baseVolumeSnapshot, targetVolumeSnapshot)
	if err != nil {
		log.Fatalf("could not get session params %v", err)
	}
	if err := client.getChangedBlocks(ctx, sessionParams); err != nil {
		log.Fatalf("could not get changed blocks %v", err)
	}
}

type Client struct {
	client  pgrpc.SnapshotMetadataClient
	kubeCli dynamic.Interface
}

func NewSnapshotMetadata() Client {
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("could not init in cluster config %v", err)
	}
	dynCli, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("failed to create dynamic client %v", err)
	}
	return Client{
		kubeCli: dynCli,
	}
}

func (c *Client) initGRPCClient(cacert []byte, URL string) {
	tlsCredentials, err := loadTLSCredentials(cacert)
	if err != nil {
		log.Fatal("cannot load TLS credentials: ", err)
	}
	conn, err := grpc.Dial(URL, grpc.WithTransportCredentials(tlsCredentials))
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	c.client = pgrpc.NewSnapshotMetadataClient(conn)

}

// Create session and get session parameters with custom resource CSISnapshotSessionAccess
func (c *Client) setupSession(ctx context.Context, baseSnap, targetSnap string) (*cbtv1alpha1.CSISnapshotSessionAccessStatus, error) {
	fmt.Printf("## Creating CSI Snapshot Session...\n\n")
	objectName, err := c.createSessionObject(ctx, baseSnap, targetSnap)
	if err != nil {
		return nil, err
	}
	retryCount := 20
	for i := 0; i < retryCount; i++ {
		status, err := c.getSessionStatus(ctx, objectName)
		if err != nil {
			return nil, err
		}
		if status.SessionState == cbtv1alpha1.SessionStateTypeReady {
			fmt.Println("Session params: ", jsonify(status))
			return status, nil
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("Timed out while waiting for CSISnapshotSessionAccess %s to be ready", objectName)

}

// Create session with CSISnapshotSessionAccess CR
func (c *Client) createSessionObject(ctx context.Context, baseSnap, targetSnap string) (string, error) {
	object := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cbt.storage.k8s.io/v1alpha1",
			"kind":       "CSISnapshotSessionAccess",
			"metadata": map[string]interface{}{
				"name": "test-" + rand.String(8),
			},
			"spec": map[string]interface{}{
				"snapshots": []interface{}{
					baseSnap,
					targetSnap,
				},
			},
		},
	}
	us, err := c.kubeCli.Resource(gvr).Namespace(clientNamespace).Create(context.TODO(), object, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return us.GetName(), nil
}

func (c *Client) getSessionStatus(ctx context.Context, objectName string) (*cbtv1alpha1.CSISnapshotSessionAccessStatus, error) {
	var ssa cbtv1alpha1.CSISnapshotSessionAccess
	us, err := c.kubeCli.Resource(gvr).Namespace(clientNamespace).Get(context.TODO(), objectName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(us.UnstructuredContent(), &ssa)
	if err != nil {
		return nil, err
	}
	fmt.Println("Session creation state: ", ssa.Status.SessionState)
	return &ssa.Status, nil
}

// Get changed blocks metadata with GetDelta rpc. The session needs to be created before making the rpc call
// The session is created using CSISnapshotSessionAccess resource
// Server auth at client side is done with CA Cert received in session params
// The token is used to in the req parameter which is used by the server to authenticate the client
func (c *Client) getChangedBlocks(ctx context.Context, sessionParams *cbtv1alpha1.CSISnapshotSessionAccessStatus) error {
	fmt.Printf("\n## Making gRPC Call on %s endpoint to Get Changed Blocks Metadata...\n\n", sessionParams.SessionURL)

	c.initGRPCClient(sessionParams.CACert, sessionParams.SessionURL)
	stream, err := c.client.GetDelta(ctx, &pgrpc.GetDeltaRequest{
		SessionToken:       string(sessionParams.SessionToken),
		BaseSnapshot:       baseVolumeSnapshot,
		TargetSnapshot:     targetVolumeSnapshot,
		StartingByteOffset: 0,
		MaxResults:         uint32(256),
	})
	if err != nil {
		return err
	}
	done := make(chan bool)
	fmt.Println("Resp received:")
	go func() {
		for {
			resp, err := stream.Recv()
			if err == io.EOF {
				done <- true //means stream is finished
				return
			}
			if err != nil {
				log.Fatalf("cannot receive %v", err)
			}
			respJson, _ := json.Marshal(resp)
			fmt.Println(string(respJson))
		}
	}()

	<-done //we will wait until all response is received
	log.Printf("finished")
	return nil
}

func loadTLSCredentials(cacert []byte) (credentials.TransportCredentials, error) {
	// Add custom CA to the cert pool
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(cacert) {
		return nil, fmt.Errorf("failed to add server CA's certificate")
	}

	config := &tls.Config{
		RootCAs: certPool,
	}
	return credentials.NewTLS(config), nil
}

func jsonify(obj interface{}) string {
	jsonBytes, _ := json.MarshalIndent(obj, "", "  ")
	return string(jsonBytes)
}
