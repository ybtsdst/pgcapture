package pgcapture

import (
	"context"
	"reflect"

	"github.com/jackc/pgtype"
	"github.com/rueian/pgcapture/pkg/pb"
	"github.com/rueian/pgcapture/pkg/source"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

const TableRegexOption = "TableRegex"

func NewConsumer(ctx context.Context, conn *grpc.ClientConn, option ConsumerOption) *Consumer {
	parameters, _ := structpb.NewStruct(map[string]interface{}{})
	if option.TableRegex != "" {
		parameters.Fields[TableRegexOption] = structpb.NewStringValue(option.TableRegex)
	}
	c := &DBLogGatewayConsumer{client: pb.NewDBLogGatewayClient(conn), init: &pb.CaptureInit{
		Uri:        option.URI,
		Parameters: parameters,
	}}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return &Consumer{Source: c}
}

type ConsumerOption struct {
	URI        string
	TableRegex string
}

type Consumer struct {
	Source source.RequeueSource
}

func (c *Consumer) Consume(mh ModelHandlers) error {
	refs := make(map[string]reflection, len(mh))
	for m, h := range mh {
		ref, err := reflectModel(m)
		if err != nil {
			return err
		}
		ref.hdl = h
		refs[ModelName(m.TableName())] = ref
	}

	changes, err := c.Source.Capture(source.Checkpoint{})
	if err != nil {
		return err
	}

	for change := range changes {
		switch m := change.Message.Type.(type) {
		case *pb.Message_Change:
			ref, ok := refs[ModelName(m.Change.Schema, m.Change.Table)]
			if !ok {
				break
			}
			if err := ref.hdl(Change{
				Op:  m.Change.Op,
				LSN: change.Checkpoint.LSN,
				New: makeModel(ref, m.Change.New),
				Old: makeModel(ref, m.Change.Old),
			}); err != nil {
				c.Source.Requeue(change.Checkpoint, err.Error())
				continue
			}
		}
		c.Source.Commit(change.Checkpoint)
	}
	return c.Source.Error()
}

func makeModel(ref reflection, fields []*pb.Field) interface{} {
	if len(fields) == 0 {
		return nil
	}
	ptr := reflect.New(ref.typ)
	val := ptr.Elem()
	var err error
	for _, f := range fields {
		i, ok := ref.idx[f.Name]
		if !ok {
			continue
		}
		if f.Value == nil {
			// do nothing
		} else if value, ok := f.Value.(*pb.Field_Binary); ok {
			err = val.Field(i).Addr().Interface().(pgtype.BinaryDecoder).DecodeBinary(ci, value.Binary)
		} else {
			err = val.Field(i).Addr().Interface().(pgtype.TextDecoder).DecodeText(ci, []byte(f.GetText()))
		}
		if err != nil {
			return err
		}
	}
	return ptr.Interface()
}

func (c *Consumer) Stop() {
	c.Source.Stop()
}

var ci = pgtype.NewConnInfo()
