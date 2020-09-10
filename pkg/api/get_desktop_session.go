package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tinyzimmer/kvdi/pkg/apis/kvdi/v1alpha1"
	"github.com/tinyzimmer/kvdi/pkg/util/apiutil"
	"github.com/tinyzimmer/kvdi/pkg/util/errors"

	"golang.org/x/net/websocket"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// swagger:operation GET /api/sessions/{namespace}/{name} Sessions getSession
// ---
// summary: Retrieve the status of the requested desktop session.
// description: Details include the PodPhase and CRD status.
// parameters:
// - name: namespace
//   in: path
//   description: The namespace of the desktop session
//   type: string
//   required: true
// - name: name
//   in: path
//   description: The name of the desktop session
//   type: string
//   required: true
// responses:
//   "200":
//     "$ref": "#/responses/getSessionResponse"
//   "400":
//     "$ref": "#/responses/error"
//   "403":
//     "$ref": "#/responses/error"
//   "404":
//     "$ref": "#/responses/error"
func (d *desktopAPI) GetDesktopSessionStatus(w http.ResponseWriter, r *http.Request) {
	desktop, err := d.getDesktopForRequest(r)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			apiutil.ReturnAPINotFound(fmt.Errorf("No desktop session %s found", apiutil.GetNamespacedNameFromRequest(r).String()), w)
			return
		}
		apiutil.ReturnAPIError(err, w)
		return
	}
	apiutil.WriteJSON(toReturnStatus(desktop), w)
}

// Session status response
// swagger:response getSessionResponse
type swaggerGetSessionResponse struct {
	// in:body
	Body map[string]interface{}
}

// swagger:operation GET /api/desktops/ws/{namespace}/{name}/status Desktops getSessionStatusWs
// ---
// summary: Retrieve status updates of the requested desktop session over a websocket.
// description: Details include the PodPhase and CRD status.
// parameters:
// - name: namespace
//   in: path
//   description: The namespace of the desktop session
//   type: string
//   required: true
// - name: name
//   in: path
//   description: The name of the desktop session
//   type: string
//   required: true
// responses:
//   "UPGRADE": {}
//   "400":
//     "$ref": "#/responses/error"
//   "403":
//     "$ref": "#/responses/error"
//   "404":
//     "$ref": "#/responses/error"
func (d *desktopAPI) GetDesktopSessionStatusWebsocket(conn *websocket.Conn) {
	defer conn.Close()

	ticker := time.NewTicker(time.Duration(2) * time.Second)
	for range ticker.C {

		desktop, err := d.getDesktopForRequest(conn.Request())
		if err != nil {
			if _, err := conn.Write(errors.ToAPIError(err).JSON()); err != nil {
				apiLogger.Error(err, "Failed to write error to websocket connection")
				return
			}
			if client.IgnoreNotFound(err) == nil {
				// If the desktop doesn't exist, we should give up entirely.
				// Other api errors are worth letting the client retry.
				return
			}
		}
		st := toReturnStatus(desktop)
		if _, err := conn.Write(st.JSON()); err != nil {
			apiLogger.Error(err, "Failed to write status to websocket connection")
			return
		}

		if st.Running && st.PodPhase == corev1.PodRunning {
			// we are done here, the client shouldn't need anything else
			return
		}

	}
}

func (d *desktopAPI) getDesktopForRequest(r *http.Request) (*v1alpha1.Desktop, error) {
	nn := apiutil.GetNamespacedNameFromRequest(r)
	found := &v1alpha1.Desktop{}
	return found, d.client.Get(context.TODO(), nn, found)
}

type desktopStatus struct {
	Running  bool            `json:"running"`
	PodPhase corev1.PodPhase `json:"podPhase"`
}

func toReturnStatus(desktop *v1alpha1.Desktop) *desktopStatus {
	return &desktopStatus{
		Running:  desktop.Status.Running,
		PodPhase: desktop.Status.PodPhase,
	}
}

func (d *desktopStatus) JSON() []byte {
	out, _ := json.Marshal(d)
	return out
}
