package backupservice

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dujiao-next/internal/upstream"
)

type mockStore struct {
	started              bool
	saveErr, deliveryErr error
	result               *upstream.FangfangPurchaseResult
	purchaseErr          error
	unbound              bool
	calls                []string
}

func (s *mockStore) Begin(context.Context, uint) (Attempt, bool, error) {
	s.calls = append(s.calls, "begin")
	attempt := Attempt{ClaimID: 1, AttemptHash: "fixed"}
	if !s.unbound {
		attempt.Client = mockClient{store: s, result: s.result, err: s.purchaseErr}
	}
	return attempt, s.started, nil
}

func TestWorkerRejectsMissingBoundClient(t *testing.T) {
	s := &mockStore{started: true, unbound: true}
	if err := (Worker{Store: s}).Run(context.Background(), 1); !errors.Is(err, ErrReconcile) {
		t.Fatal("unbound attempt did not fail closed")
	}
	if !reflect.DeepEqual(s.calls, []string{"begin", "uncertain"}) {
		t.Fatalf("unexpected calls: %v", s.calls)
	}
}
func (s *mockStore) SaveReceipt(_ context.Context, _ Attempt, _ int, _ []byte) error {
	s.calls = append(s.calls, "save")
	return s.saveErr
}
func (s *mockStore) MarkUncertain(context.Context, Attempt) error {
	s.calls = append(s.calls, "uncertain")
	return nil
}
func (s *mockStore) DeliverSaved(context.Context, Attempt) error {
	s.calls = append(s.calls, "deliver")
	return s.deliveryErr
}

type mockClient struct {
	store  *mockStore
	result *upstream.FangfangPurchaseResult
	err    error
}

func (c mockClient) PurchaseOnce(context.Context, upstream.FangfangPurchaseRequest) (*upstream.FangfangPurchaseResult, error) {
	c.store.calls = append(c.store.calls, "purchase")
	return c.result, c.err
}

func TestWorkerDurableReceiptBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		started                           bool
		response                          bool
		purchaseErr, saveErr, deliveryErr error
		want                              []string
		wantErr                           bool
	}{
		{"success", true, true, nil, nil, nil, []string{"begin", "purchase", "save", "deliver"}, false},
		{"duplicate", false, true, nil, nil, nil, []string{"begin"}, false},
		{"supplier uncertainty with receipt", true, true, errors.New("secret"), nil, nil, []string{"begin", "purchase", "save", "uncertain"}, true},
		{"lost response", true, false, errors.New("secret"), nil, nil, []string{"begin", "purchase", "uncertain"}, true},
		{"receipt failure", true, true, nil, errors.New("secret"), nil, []string{"begin", "purchase", "save", "uncertain"}, true},
		{"delivery failure", true, true, nil, nil, errors.New("secret"), []string{"begin", "purchase", "save", "deliver"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockStore{started: tc.started, saveErr: tc.saveErr, deliveryErr: tc.deliveryErr, purchaseErr: tc.purchaseErr}
			var response *upstream.FangfangPurchaseResult
			if tc.response {
				response = &upstream.FangfangPurchaseResult{HTTPStatus: 200, RawResponse: []byte("synthetic secret")}
			}
			s.result = response
			err := (Worker{Store: s}).Run(context.Background(), 1)
			if (err != nil) != tc.wantErr || !reflect.DeepEqual(s.calls, tc.want) {
				t.Fatalf("got calls %v, error %v", s.calls, err)
			}
		})
	}
}
