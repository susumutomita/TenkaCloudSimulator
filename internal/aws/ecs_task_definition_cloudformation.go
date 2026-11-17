package aws

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
)

const stackTaskDefinition = "AWS::ECS::TaskDefinition"
const stackECSAccount = "000000000000"

// Convert the supported CloudFormation property names to the native API input.
// LogConfiguration.Options is a map whose user-provided keys keep their case.
func stackECSTaskInput(value any) (any, error) {
	switch x := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for name, v := range x {
			switch name {
			case "Options":
				out["options"] = v
				continue
			case "Family", "Cpu", "Memory", "NetworkMode", "RequiresCompatibilities", "ExecutionRoleArn", "TaskRoleArn", "ContainerDefinitions",
				"Name", "Essential", "Image", "PortMappings", "ContainerPort", "HostPort", "Protocol", "AppProtocol", "Environment", "Value",
				"Secrets", "ValueFrom", "LogConfiguration", "LogDriver", "Tags", "Key":
			default:
				return nil, fmt.Errorf("unsupported task definition property %q", name)
			}
			native, err := stackECSTaskInput(v)
			if err != nil {
				return nil, err
			}
			out[strings.ToLower(name[:1])+name[1:]] = native
		}
		return out, nil
	case []any:
		items := make([]any, 0, len(x))
		for _, v := range x {
			item, err := stackECSTaskInput(v)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return value, nil
	}
}

func prepareStackECSTask(region string, properties map[string]any, revision int) (*catalog.StackTaskDefinition, error) {
	input, err := stackECSTaskInput(properties)
	if err != nil {
		return nil, err
	}
	return catalog.PrepareStackTaskDefinition(region, stackECSAccount, revision, input.(map[string]any))
}

func plannedStackECSTask(region string, r StackResource) (*catalog.StackTaskDefinition, error) {
	_, revisionText, found := strings.Cut(r.PhysicalID[strings.LastIndex(r.PhysicalID, "/")+1:], ":")
	revision, err := strconv.Atoi(revisionText)
	if !found || err != nil || revision < 1 {
		return nil, ssmFailure{"ValidationError", "Invalid task definition revision in stack record."}
	}
	task, err := prepareStackECSTask(region, r.Properties, revision)
	if err != nil {
		return nil, ssmFailure{"ValidationError", err.Error()}
	}
	if task.ARN() != r.PhysicalID {
		return nil, ssmFailure{"ValidationError", "Task definition differs from the planned ARN."}
	}
	return task, nil
}

func createStackECSTask(tx *sql.Tx, record StackRecord, r StackResource) error {
	task, err := plannedStackECSTask(record.Region, r)
	if err != nil {
		return err
	}
	if err = task.Register(tx); err != nil {
		return ssmFailure{"ValidationError", err.Error()}
	}
	_, err = tx.Exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(record.Region, r.Type), r.Type, r.PhysicalID, record.ID)
	return err
}
