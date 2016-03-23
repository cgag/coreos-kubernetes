package cluster

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"regexp"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/ec2"

	"github.com/coreos/coreos-kubernetes/multi-node/aws/pkg/config"
)

// set by build script
var VERSION = "UNKNOWN"

type ClusterInfo struct {
	Name         string
	ControllerIP string
}

func (c *ClusterInfo) String() string {
	buf := new(bytes.Buffer)
	w := new(tabwriter.Writer)
	w.Init(buf, 0, 8, 0, '\t', 0)

	fmt.Fprintf(w, "Cluster Name:\t%s\n", c.Name)
	fmt.Fprintf(w, "Controller IP:\t%s\n", c.ControllerIP)

	w.Flush()
	return buf.String()
}

func New(cfg *config.Cluster, awsDebug bool) *Cluster {
	awsConfig := aws.NewConfig()
	awsConfig = awsConfig.WithRegion(cfg.Region)
	if awsDebug {
		awsConfig = awsConfig.WithLogLevel(aws.LogDebug)
	}

	return &Cluster{
		Cluster: *cfg,
		session: session.New(awsConfig),
	}
}

type Cluster struct {
	config.Cluster
	session *session.Session
}

func (c *Cluster) ValidateStack(stackBody string) (string, error) {
	validateInput := cloudformation.ValidateTemplateInput{
		TemplateBody: &stackBody,
	}

	cfSvc := cloudformation.New(c.session)
	validationReport, err := cfSvc.ValidateTemplate(&validateInput)
	if err != nil {
		return "", fmt.Errorf("invalid cloudformation stack: %v", err)
	}

	describeStacksInput := cloudformation.DescribeStacksInput{
		StackName: aws.String(c.ClusterName),
	}

	//Find out if stack exists already. This determines whether we should do subnet conflict validatio
	var stackExists bool
	stackNotExistExpr := regexp.MustCompile(fmt.Sprintf("^ValidationError: Stack with id %s does not exist", c.ClusterName))

	describeStacksOutput, err := cfSvc.DescribeStacks(&describeStacksInput)
	if err != nil {
		if stackNotExistExpr.Match([]byte(err.Error())) {
			//No results for a list operation is not an error!!! (unless your AWS)
			stackExists = false
		} else {
			return "", fmt.Errorf("error describing stack: %v", err)
		}
	} else {
		stackExists = true
		if len(describeStacksOutput.Stacks) > 1 {
			return "", fmt.Errorf("expected error: found more the one stack with name %s", c.ClusterName)
		}
	}

	//if the stack already exists, is doesn't make sense to validate the existing VPC for subnet conflicts
	if c.VPCID != "" && !stackExists {
		fmt.Println("Existing VPC detected. Will validate for subnet cidr conflicts")
		if err := c.validateExistingVPC(); err != nil {
			return "", err
		}
	}

	return validationReport.String(), nil
}

func (c *Cluster) validateExistingVPC() error {
	ec2Svc := ec2.New(c.session)

	describeVpcsInput := ec2.DescribeVpcsInput{
		VpcIds: []*string{aws.String(c.VPCID)},
	}
	vpcOutput, err := ec2Svc.DescribeVpcs(&describeVpcsInput)
	if err != nil {
		return fmt.Errorf("error describing existing vpc: %v", err)
	}
	if len(vpcOutput.Vpcs) == 0 {
		return fmt.Errorf("could not find vpc %s in region %s", c.VPCID, c.Region)
	}
	if len(vpcOutput.Vpcs) > 1 {
		//Theoretically this should never happen. If it does, we probably want to know.
		return fmt.Errorf("found more than one vpc with id %s. this is NOT NORMAL.", c.VPCID)
	}

	existingVPC := vpcOutput.Vpcs[0]

	if *existingVPC.CidrBlock != c.VPCCIDR {
		//If this is the case, our network config validation cannot be trusted and we must abort
		return fmt.Errorf("configured vpcCidr (%s) does not match actual existing vpc cidr (%s)", c.VPCCIDR, *existingVPC.CidrBlock)
	}

	describeSubnetsInput := ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{existingVPC.VpcId},
			},
		},
	}

	subnetOutput, err := ec2Svc.DescribeSubnets(&describeSubnetsInput)
	if err != nil {
		return fmt.Errorf("error describing subnets for vpc: %v", err)
	}

	//Config validation has already ensured this subnet is contained by the existing VPC CIDR
	//We need access to the net objects so we can detect conflicts
	subnetIP, subnetNet, err := net.ParseCIDR(c.InstanceCIDR)
	if err != nil {
		return fmt.Errorf("error parsing instances cidr %s : %v", c.InstanceCIDR, err)
	}

	//Loop through all existing subnets in the VPC and look for conflicting CIDRS
	for _, existingSubnet := range subnetOutput.Subnets {
		existingSubnetIP, existingSubnetNet, err := net.ParseCIDR(*existingSubnet.CidrBlock)
		if err != nil {
			return fmt.Errorf("error parsing existing subnet cidr %s : %v", *existingSubnet.CidrBlock, err)
		}

		if existingSubnetNet.Contains(subnetIP) || subnetNet.Contains(existingSubnetIP) {
			return fmt.Errorf("instance cidr (%s) conflicts with existing subnet %s, cidr=%s", subnetNet, *existingSubnet.SubnetId, existingSubnetNet)
		}
	}

	return nil
}

func (c *Cluster) Create(stackBody string) error {
	cfSvc := cloudformation.New(c.session)
	creq := &cloudformation.CreateStackInput{
		StackName:    aws.String(c.ClusterName),
		OnFailure:    aws.String("DO_NOTHING"),
		Capabilities: []*string{aws.String(cloudformation.CapabilityCapabilityIam)},
		TemplateBody: &stackBody,
	}

	resp, err := cfSvc.CreateStack(creq)
	if err != nil {
		return err
	}

	req := cloudformation.DescribeStacksInput{
		StackName: resp.StackId,
	}
	for {
		resp, err := cfSvc.DescribeStacks(&req)
		if err != nil {
			return err
		}
		if len(resp.Stacks) == 0 {
			return fmt.Errorf("stack not found")
		}
		statusString := aws.StringValue(resp.Stacks[0].StackStatus)
		switch statusString {
		case cloudformation.ResourceStatusCreateComplete:
			return nil
		case cloudformation.ResourceStatusCreateFailed:
			errMsg := fmt.Sprintf("Stack creation failed: %s : %s", statusString, aws.StringValue(resp.Stacks[0].StackStatusReason))
			return errors.New(errMsg)
		case cloudformation.ResourceStatusCreateInProgress:
			time.Sleep(3 * time.Second)
			continue
		default:
			return fmt.Errorf("unexpected stack status: %s", statusString)
		}
	}
}

func (c *Cluster) Update(stackBody string) (string, error) {
	cfSvc := cloudformation.New(c.session)
	input := &cloudformation.UpdateStackInput{
		Capabilities: []*string{aws.String(cloudformation.CapabilityCapabilityIam)},
		StackName:    aws.String(c.ClusterName),
		TemplateBody: &stackBody,
	}

	updateOutput, err := cfSvc.UpdateStack(input)
	if err != nil {
		return "", fmt.Errorf("error updating cloudformation stack: %v", err)
	}
	req := cloudformation.DescribeStacksInput{
		StackName: updateOutput.StackId,
	}
	for {
		resp, err := cfSvc.DescribeStacks(&req)
		if err != nil {
			return "", err
		}
		if len(resp.Stacks) == 0 {
			return "", fmt.Errorf("stack not found")
		}
		statusString := aws.StringValue(resp.Stacks[0].StackStatus)
		switch statusString {
		case cloudformation.ResourceStatusUpdateComplete:
			return updateOutput.String(), nil
		case cloudformation.ResourceStatusUpdateFailed, cloudformation.StackStatusUpdateRollbackComplete, cloudformation.StackStatusUpdateRollbackFailed:
			errMsg := fmt.Sprintf("Stack status: %s : %s", statusString, aws.StringValue(resp.Stacks[0].StackStatusReason))
			return "", errors.New(errMsg)
		case cloudformation.ResourceStatusUpdateInProgress:
			time.Sleep(3 * time.Second)
			continue
		default:
			return "", fmt.Errorf("unexpected stack status: %s", statusString)
		}
	}
}

func (c *Cluster) Info() (*ClusterInfo, error) {
	resources := make([]cloudformation.StackResourceSummary, 0)
	req := cloudformation.ListStackResourcesInput{
		StackName: aws.String(c.ClusterName),
	}
	cfSvc := cloudformation.New(c.session)
	for {
		resp, err := cfSvc.ListStackResources(&req)
		if err != nil {
			return nil, err
		}
		for _, s := range resp.StackResourceSummaries {
			resources = append(resources, *s)
		}
		req.NextToken = resp.NextToken
		if aws.StringValue(req.NextToken) == "" {
			break
		}
	}

	var info ClusterInfo
	for _, r := range resources {
		switch aws.StringValue(r.LogicalResourceId) {
		case "EIPController":
			if r.PhysicalResourceId != nil {
				info.ControllerIP = *r.PhysicalResourceId
			} else {
				return nil, fmt.Errorf("unable to get public IP of controller instance")
			}
		}
	}

	return &info, nil
}

func (c *Cluster) Destroy() error {
	cfSvc := cloudformation.New(c.session)
	dreq := &cloudformation.DeleteStackInput{
		StackName: aws.String(c.ClusterName),
	}
	_, err := cfSvc.DeleteStack(dreq)
	return err
}
