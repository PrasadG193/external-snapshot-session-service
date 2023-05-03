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
	pgrpc "github.com/PrasadG193/sample-ext-snap-session-svc/pkg/grpc"
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

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := NewSnapshotMetadata()
	sessionParams, err := client.GetCSISnapSessionParams(ctx, "vs-005", "vs-006")
	if err != nil {
		log.Fatalf("could not get session params %v", err)
	}
	if err := client.GetChangedBlocks(ctx, sessionParams); err != nil {
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

func (c *Client) GetCSISnapSessionParams(ctx context.Context, baseSnap, targetSnap string) (*cbtv1alpha1.CSISnapshotSessionAccessStatus, error) {
	fmt.Printf("## Creating CSI Snapshot Session...\n\n")
	gvr := schema.GroupVersionResource{Group: "cbt.storage.k8s.io", Version: "v1alpha1", Resource: "csisnapshotsessionaccesses"}
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
	us, err := c.kubeCli.Resource(gvr).Namespace("cbt-test").Create(context.TODO(), object, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	retryCount := 20
	for i := 0; i < retryCount; i++ {
		var ssa cbtv1alpha1.CSISnapshotSessionAccess
		us, err := c.kubeCli.Resource(gvr).Namespace("cbt-test").Get(context.TODO(), us.GetName(), metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(us.UnstructuredContent(), &ssa)
		if err != nil {
			return nil, err
		}
		fmt.Println("Session creation state: ", ssa.Status.SessionState)
		if ssa.Status.SessionState == cbtv1alpha1.SessionStateTypeReady {
			statusJson, _ := json.MarshalIndent(ssa.Status, "", "  ")
			fmt.Println("Session params: ", string(statusJson))
			return &ssa.Status, nil
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("Timed out while waiting for CSISnapshotSessionAccess %s/%s to be ready", us.GetNamespace(), us.GetName())

}

func (c *Client) GetChangedBlocks(ctx context.Context, sessionParams *cbtv1alpha1.CSISnapshotSessionAccessStatus) error {
	fmt.Printf("\n## Making gRPC Call on %s endpoint to Get Changed Blocks Metadata...\n\n", sessionParams.SessionURL)
	c.initGRPCClient(sessionParams.CACert, sessionParams.SessionURL)
	stream, err := c.client.GetDelta(ctx, &pgrpc.GetDeltaRequest{
		SessionToken:       string(sessionParams.SessionToken),
		BaseSnapshot:       "vs-005",
		TargetSnapshot:     "vs-006",
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
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(cacert) {
		return nil, fmt.Errorf("failed to add server CA's certificate")
	}

	// Create the credentials and return it
	config := &tls.Config{
		RootCAs: certPool,
	}

	return credentials.NewTLS(config), nil
}
