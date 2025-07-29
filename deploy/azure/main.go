package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/attestation/armattestation"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v5"
	"github.com/spf13/cobra"
)

const (
	projectTag = "SurgeTDX"
)

type DeploymentInfo struct {
	ID              string    `json:"id"`
	SubscriptionID  string    `json:"subscription_id"`
	TenantID        string    `json:"tenant_id"`
	ResourceGroup   string    `json:"resource_group"`
	VMName          string    `json:"vm_name"`
	OSDiskName      string    `json:"os_disk_name"`
	StorageDiskName string    `json:"storage_disk_name"`
	NSGName         string    `json:"nsg_name"`
	PublicIPName    string    `json:"public_ip_name"`
	NICName         string    `json:"nic_name"`
	VNetName        string    `json:"vnet_name,omitempty"`
	SubnetName      string    `json:"subnet_name,omitempty"`
	AttestationName string    `json:"attestation_name,omitempty"`
	Location        string    `json:"location"`
	CreatedAt       time.Time `json:"created_at"`
}

type AzureClient struct {
	cred           azcore.TokenCredential
	tenantID       string
	subscriptionID string
	ctx            context.Context
	computeClient  *armcompute.DisksClient
	vmClient       *armcompute.VirtualMachinesClient
	networkClient  *armnetwork.SecurityGroupsClient
	nsgRulesClient *armnetwork.SecurityRulesClient
	nicClient      *armnetwork.InterfacesClient
	publicIPClient *armnetwork.PublicIPAddressesClient
	vnetClient     *armnetwork.VirtualNetworksClient
	subnetClient   *armnetwork.SubnetsClient
	attestClient   *armattestation.ProvidersClient
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "surgetdx-vm",
		Short: "Azure VM deployment tool for SurgeTDX",
	}

	deployCmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy a new VM",
		RunE:  deployCommand,
	}

	deleteCmd := &cobra.Command{
		Use:   "delete [deployment-id]",
		Short: "Delete a deployment",
		Args:  cobra.ExactArgs(1),
		RunE:  deleteCommand,
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all deployments",
		RunE:  listCommand,
	}

	// Deploy command flags
	deployCmd.Flags().String("id", "", "Deployment ID (required)")
	deployCmd.Flags().String("disk-path", "", "Path to disk image (required)")
	deployCmd.Flags().String("resource-group", "", "Azure resource group (required)")
	deployCmd.Flags().String("region", "", "Azure region (required)")
	deployCmd.Flags().String("vm-size", "Standard_EC4es_v5", "Azure VM size")
	deployCmd.Flags().Int("storage-gb", 100, "Storage disk size in GB")
	deployCmd.Flags().String("allowed-ip", "*", "Allowed IP for SSH")
	deployCmd.Flags().String("vnet-name", "", "Virtual network name (optional, will try to find one)")
	deployCmd.Flags().String("subnet-name", "default", "Subnet name (default: 'default')")
	deployCmd.Flags().String("subscription-id", "", "Azure subscription ID")
	deployCmd.Flags().String("tenant-id", "", "Azure tenant ID")

	deployCmd.MarkFlagRequired("id")
	deployCmd.MarkFlagRequired("disk-path")
	deployCmd.MarkFlagRequired("resource-group")
	deployCmd.MarkFlagRequired("region")
	deployCmd.MarkFlagRequired("subscription-id")
	deployCmd.MarkFlagRequired("tenant-id")

	rootCmd.AddCommand(deployCmd, deleteCmd, listCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func createAzureClient(ctx context.Context, tenantID, subscriptionID string) (*AzureClient, error) {
	cred, err := azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: tenantID})
	if err != nil {
		return nil, fmt.Errorf("failed to obtain credentials: %w", err)
	}

	client := &AzureClient{
		cred:           cred,
		tenantID:       tenantID,
		subscriptionID: subscriptionID,
		ctx:            ctx,
	}

	// Initialize clients
	client.computeClient, err = armcompute.NewDisksClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.vmClient, err = armcompute.NewVirtualMachinesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.networkClient, err = armnetwork.NewSecurityGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.nsgRulesClient, err = armnetwork.NewSecurityRulesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.nicClient, err = armnetwork.NewInterfacesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.publicIPClient, err = armnetwork.NewPublicIPAddressesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.vnetClient, err = armnetwork.NewVirtualNetworksClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.subnetClient, err = armnetwork.NewSubnetsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	client.attestClient, err = armattestation.NewProvidersClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func deployCommand(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Get flags
	deploymentID, _ := cmd.Flags().GetString("id")
	diskPath, _ := cmd.Flags().GetString("disk-path")
	resourceGroup, _ := cmd.Flags().GetString("resource-group")
	region, _ := cmd.Flags().GetString("region")
	vmSize, _ := cmd.Flags().GetString("vm-size")
	storageGB, _ := cmd.Flags().GetInt("storage-gb")
	allowedIP, _ := cmd.Flags().GetString("allowed-ip")
	vnetName, _ := cmd.Flags().GetString("vnet-name")
	subnetName, _ := cmd.Flags().GetString("subnet-name")
	subscriptionID, _ := cmd.Flags().GetString("subscription-id")
	tenantID, _ := cmd.Flags().GetString("tenant-id")

	// Validate deployment ID doesn't exist
	deploymentFile := getDeploymentFile(deploymentID)
	if _, err := os.Stat(deploymentFile); err == nil {
		return fmt.Errorf("deployment with ID '%s' already exists", deploymentID)
	}

	// Get disk size
	diskInfo, err := os.Stat(diskPath)
	if err != nil {
		return fmt.Errorf("failed to stat disk file: %w", err)
	}
	diskSize := diskInfo.Size()

	// Create Azure client
	client, err := createAzureClient(ctx, tenantID, subscriptionID)
	if err != nil {
		return err
	}

	// Create deployment info
	vmName := fmt.Sprintf("surgetdx-%s", deploymentID)
	deployment := DeploymentInfo{
		ID:              deploymentID,
		TenantID:        tenantID,
		SubscriptionID:  subscriptionID,
		ResourceGroup:   resourceGroup,
		VMName:          vmName,
		OSDiskName:      vmName,
		StorageDiskName: fmt.Sprintf("%s-storage", vmName),
		NSGName:         vmName,
		PublicIPName:    fmt.Sprintf("%s-ip", vmName),
		Location:        region,
		CreatedAt:       time.Now(),
	}

	// Display deployment configuration
	fmt.Printf("\n📋 Deployment Configuration:\n")
	fmt.Printf("─────────────────────────────────────────────────────────────\n")
	fmt.Printf("   Deployment ID:     %s\n", deploymentID)
	fmt.Printf("   Subscription ID:   %s\n", subscriptionID)
	fmt.Printf("   Resource Group:    %s\n", resourceGroup)
	fmt.Printf("   Region:            %s\n", region)
	fmt.Printf("   VM Name:           %s\n", vmName)
	fmt.Printf("   VM Size:           %s\n", vmSize)
	fmt.Printf("   Disk Image:        %s (%d GB)\n", diskPath, bytesToGB(diskSize))
	fmt.Printf("   Storage Disk:      %d GB\n", storageGB)
	fmt.Printf("   SSH Allowed IP:    %s\n", allowedIP)
	if vnetName != "" {
		fmt.Printf("   VNet Name:         %s\n", vnetName)
	} else {
		fmt.Printf("   VNet Name:         <auto-detect>\n")
	}
	fmt.Printf("   Subnet Name:       %s\n", subnetName)
	fmt.Printf("\n🏗️  Resources to be created:\n")
	fmt.Printf("   • VM:              %s\n", vmName)
	fmt.Printf("   • OS Disk:         %s\n", deployment.OSDiskName)
	fmt.Printf("   • Storage Disk:    %s\n", deployment.StorageDiskName)
	fmt.Printf("   • Network SG:      %s\n", deployment.NSGName)
	fmt.Printf("   • Public IP:       %s-ip\n", vmName)
	fmt.Printf("   • Network Interface: %s-nic\n", vmName)
	if vnetName != "" {
		fmt.Printf("   • Virtual Network: %s\n", vnetName)
	} else {
		fmt.Printf("   • Virtual Network: %s-vnet (auto-created)\n", vmName)
	}
	fmt.Printf("   • Subnet:          %s\n", subnetName)
	fmt.Printf("─────────────────────────────────────────────────────────────\n")

	fmt.Printf("\n🚀 Starting deployment '%s' in resource group '%s'...\n", deploymentID, resourceGroup)

	// Create OS Disk
	fmt.Println("📦 Creating OS disk...")
	if err := createOSDisk(client, deployment, diskSize); err != nil {
		return fmt.Errorf("failed to create OS disk: %w", err)
	}

	// Upload disk image
	fmt.Println("⬆️  Uploading disk image...")
	if err := uploadDiskImage(client, deployment, diskPath); err != nil {
		return fmt.Errorf("failed to upload disk image: %w", err)
	}

	// Create storage disk
	fmt.Println("💾 Creating storage disk...")
	if err := createStorageDisk(client, deployment, int32(storageGB)); err != nil {
		return fmt.Errorf("failed to create storage disk: %w", err)
	}

	// Create NSG
	fmt.Println("🔒 Creating network security group...")
	if err := createNSG(client, deployment, allowedIP); err != nil {
		return fmt.Errorf("failed to create NSG: %w", err)
	}

	// Create VM
	fmt.Println("🖥️  Creating virtual machine...")
	if err := createVM(client, &deployment, vmSize, "", vnetName, subnetName); err != nil {
		return fmt.Errorf("failed to create VM: %w", err)
	}

	// Update deployment info with public IP name for later retrieval
	if err := updatePublicIPInfo(client, &deployment); err != nil {
		fmt.Printf("⚠️  Warning: Could not retrieve public IP info: %v\n", err)
	}

	// Save deployment info
	if err := saveDeploymentInfo(deployment); err != nil {
		return fmt.Errorf("failed to save deployment info: %w", err)
	}

	// Get the actual public IP address for display
	publicIPAddress := ""
	if deployment.PublicIPName != "" {
		ipResp, err := client.publicIPClient.Get(client.ctx, deployment.ResourceGroup, deployment.PublicIPName, nil)
		if err == nil && ipResp.Properties != nil && ipResp.Properties.IPAddress != nil {
			publicIPAddress = *ipResp.Properties.IPAddress
		}
	}

	fmt.Println("\n✅ Deployment completed successfully!")
	fmt.Printf("\n📋 Deployment Details:\n")
	fmt.Printf("   ID: %s\n", deployment.ID)
	fmt.Printf("   VM Name: %s\n", deployment.VMName)
	fmt.Printf("   Resource Group: %s\n", deployment.ResourceGroup)
	fmt.Printf("   Location: %s\n", deployment.Location)
	if publicIPAddress != "" {
		fmt.Printf("   Public IP Address: %s\n", publicIPAddress)
	}
	fmt.Printf("\n💻 Connection:\n")
	if publicIPAddress != "" {
		fmt.Printf("   SSH to your VM using: ssh <username>@%s\n", publicIPAddress)
	} else {
		fmt.Printf("   SSH to your VM using: ssh <username>@<public-ip>\n")
		fmt.Printf("   (Check Azure Portal for the public IP address)\n")
	}
	fmt.Printf("\n🗑️  To delete this deployment:\n")
	fmt.Printf("   surgetdx-vm delete %s\n", deploymentID)

	return nil
}

func createOSDisk(client *AzureClient, deployment DeploymentInfo, diskSize int64) error {
	disk := armcompute.Disk{
		Location: to.Ptr(deployment.Location),
		Properties: &armcompute.DiskProperties{
			OSType: to.Ptr(armcompute.OperatingSystemTypesLinux),
			CreationData: &armcompute.CreationData{
				CreateOption:    to.Ptr(armcompute.DiskCreateOptionUpload),
				UploadSizeBytes: to.Ptr(diskSize),
			},
			HyperVGeneration: to.Ptr(armcompute.HyperVGenerationV2),
			SecurityProfile: &armcompute.DiskSecurityProfile{
				SecurityType: to.Ptr(armcompute.DiskSecurityTypesConfidentialVMNonPersistedTPM),
			},
		},
		SKU: &armcompute.DiskSKU{
			Name: to.Ptr(armcompute.DiskStorageAccountTypesStandardLRS),
		},
		Tags: map[string]*string{
			"Project": to.Ptr(projectTag),
			"VM":      to.Ptr(deployment.VMName),
		},
	}

	poller, err := client.computeClient.BeginCreateOrUpdate(
		client.ctx,
		deployment.ResourceGroup,
		deployment.OSDiskName,
		disk,
		nil,
	)
	if err != nil {
		return err
	}

	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func uploadDiskImage(client *AzureClient, deployment DeploymentInfo, diskPath string) error {
	// Grant access to get SAS URL
	accessLevel := armcompute.AccessLevelWrite
	poller, err := client.computeClient.BeginGrantAccess(
		client.ctx,
		deployment.ResourceGroup,
		deployment.OSDiskName,
		armcompute.GrantAccessData{
			Access:            to.Ptr(accessLevel),
			DurationInSeconds: to.Ptr(int32(86400)),
		},
		nil,
	)
	if err != nil {
		return err
	}

	resp, err := poller.PollUntilDone(client.ctx, nil)
	if err != nil {
		return err
	}

	sasURL := *resp.AccessURI.AccessSAS

	// Use azcopy to upload
	cmd := exec.Command("azcopy", "copy", diskPath, sasURL, "--blob-type", "PageBlob")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Revoke access even if upload fails
		client.computeClient.BeginRevokeAccess(client.ctx, deployment.ResourceGroup, deployment.OSDiskName, nil)
		return fmt.Errorf("azcopy failed: %w", err)
	}

	// Revoke access
	revokePoller, err := client.computeClient.BeginRevokeAccess(
		client.ctx,
		deployment.ResourceGroup,
		deployment.OSDiskName,
		nil,
	)
	if err != nil {
		return err
	}

	_, err = revokePoller.PollUntilDone(client.ctx, nil)
	return err
}

func createStorageDisk(client *AzureClient, deployment DeploymentInfo, sizeGB int32) error {
	disk := armcompute.Disk{
		Location: to.Ptr(deployment.Location),
		Properties: &armcompute.DiskProperties{
			CreationData: &armcompute.CreationData{
				CreateOption: to.Ptr(armcompute.DiskCreateOptionEmpty),
			},
			DiskSizeGB: to.Ptr(sizeGB),
		},
		SKU: &armcompute.DiskSKU{
			Name: to.Ptr(armcompute.DiskStorageAccountTypesStandardSSDLRS),
		},
		Tags: map[string]*string{
			"Project": to.Ptr(projectTag),
			"VM":      to.Ptr(deployment.VMName),
		},
	}

	poller, err := client.computeClient.BeginCreateOrUpdate(
		client.ctx,
		deployment.ResourceGroup,
		deployment.StorageDiskName,
		disk,
		nil,
	)
	if err != nil {
		return err
	}

	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func createNSG(client *AzureClient, deployment DeploymentInfo, allowedIP string) error {
	// Create NSG
	nsg := armnetwork.SecurityGroup{
		Location: to.Ptr(deployment.Location),
		Tags: map[string]*string{
			"Project": to.Ptr(projectTag),
			"VM":      to.Ptr(deployment.VMName),
		},
	}

	nsgPoller, err := client.networkClient.BeginCreateOrUpdate(
		client.ctx,
		deployment.ResourceGroup,
		deployment.NSGName,
		nsg,
		nil,
	)
	if err != nil {
		return err
	}

	_, err = nsgPoller.PollUntilDone(client.ctx, nil)
	if err != nil {
		return err
	}

	// Create NSG rules
	rules := []struct {
		name     string
		priority int32
		port     string
		sourceIP string
		protocol armnetwork.SecurityRuleProtocol
	}{
		{"AllowSSH", 100, "22", allowedIP, armnetwork.SecurityRuleProtocolTCP},
		{"TCP8545", 110, "8545", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8551", 111, "8551", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8645", 112, "8645", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8745", 113, "8745", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8018", 114, "8018", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8547", 115, "8547", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8548", 116, "8548", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8552", 117, "8552", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP8080", 118, "8080", "*", armnetwork.SecurityRuleProtocolTCP},
		{"TCP30313", 119, "30313", "*", armnetwork.SecurityRuleProtocolTCP},
		{"ANY30303", 120, "30303", "*", armnetwork.SecurityRuleProtocolAsterisk},
	}

	for _, rule := range rules {
		securityRule := armnetwork.SecurityRule{
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Priority:                 to.Ptr(rule.priority),
				Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
				Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
				Protocol:                 to.Ptr(rule.protocol),
				SourceAddressPrefix:      to.Ptr(rule.sourceIP),
				SourcePortRange:          to.Ptr("*"),
				DestinationAddressPrefix: to.Ptr("*"),
				DestinationPortRange:     to.Ptr(rule.port),
			},
		}

		rulePoller, err := client.nsgRulesClient.BeginCreateOrUpdate(
			client.ctx,
			deployment.ResourceGroup,
			deployment.NSGName,
			rule.name,
			securityRule,
			nil,
		)
		if err != nil {
			return fmt.Errorf("failed to create rule %s: %w", rule.name, err)
		}

		_, err = rulePoller.PollUntilDone(client.ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to create rule %s: %w", rule.name, err)
		}
	}

	return nil
}

func createAttestationProvider(client *AzureClient, deployment DeploymentInfo) (string, string, error) {
	// Generate random suffix for attestation name
	randBytes := make([]byte, 6)
	rand.Read(randBytes)
	attestName := strings.ReplaceAll(deployment.VMName, "-", "") + hex.EncodeToString(randBytes)

	resp, err := client.attestClient.Create(
		client.ctx,
		deployment.ResourceGroup,
		attestName,
		armattestation.ServiceCreationParams{
			Location: to.Ptr(deployment.Location),
			Tags: map[string]*string{
				"Project": to.Ptr(projectTag),
				"VM":      to.Ptr(deployment.VMName),
			},
		},
		nil,
	)
	if err != nil {
		return "", "", err
	}

	return attestName, *resp.Properties.AttestURI, nil
}

func findOrCreateSubnet(client *AzureClient, deployment DeploymentInfo, vnetName, subnetName string) (*armnetwork.Subnet, string, error) {
	// If no vnet name provided, use a default name
	if vnetName == "" {
		vnetName = fmt.Sprintf("%s-vnet", deployment.VMName)
	}

	// Try to get existing VNet
	_, err := client.vnetClient.Get(client.ctx, deployment.ResourceGroup, vnetName, nil)
	if err != nil {
		// VNet doesn't exist, create it
		fmt.Printf("   Creating virtual network: %s\n", vnetName)
		newVNet := armnetwork.VirtualNetwork{
			Location: to.Ptr(deployment.Location),
			Properties: &armnetwork.VirtualNetworkPropertiesFormat{
				AddressSpace: &armnetwork.AddressSpace{
					AddressPrefixes: []*string{
						to.Ptr("10.0.0.0/16"),
					},
				},
			},
			Tags: map[string]*string{
				"Project": to.Ptr(projectTag),
				"VM":      to.Ptr(deployment.VMName),
			},
		}

		vnetPoller, err := client.vnetClient.BeginCreateOrUpdate(
			client.ctx,
			deployment.ResourceGroup,
			vnetName,
			newVNet,
			nil,
		)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create VNet: %w", err)
		}

		_, err = vnetPoller.PollUntilDone(client.ctx, nil)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create VNet: %w", err)
		}
	}

	// Try to get existing subnet
	subnet, err := client.subnetClient.Get(client.ctx, deployment.ResourceGroup, vnetName, subnetName, nil)
	if err != nil {
		// Subnet doesn't exist, create it
		fmt.Printf("   Creating subnet: %s\n", subnetName)
		newSubnet := armnetwork.Subnet{
			Properties: &armnetwork.SubnetPropertiesFormat{
				AddressPrefix: to.Ptr("10.0.0.0/24"),
			},
		}

		subnetPoller, err := client.subnetClient.BeginCreateOrUpdate(
			client.ctx,
			deployment.ResourceGroup,
			vnetName,
			subnetName,
			newSubnet,
			nil,
		)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create subnet: %w", err)
		}

		subnetResp, err := subnetPoller.PollUntilDone(client.ctx, nil)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create subnet: %w", err)
		}
		return &subnetResp.Subnet, vnetName, nil
	}

	return &subnet.Subnet, vnetName, nil
}

func createVM(client *AzureClient, deployment *DeploymentInfo, vmSize, userData, vnetName, subnetName string) error {
	// Get disk resources
	osDisk, err := client.computeClient.Get(client.ctx, deployment.ResourceGroup, deployment.OSDiskName, nil)
	if err != nil {
		return fmt.Errorf("failed to get OS disk: %w", err)
	}

	storageDisk, err := client.computeClient.Get(client.ctx, deployment.ResourceGroup, deployment.StorageDiskName, nil)
	if err != nil {
		return fmt.Errorf("failed to get storage disk: %w", err)
	}

	// Get NSG
	nsg, err := client.networkClient.Get(client.ctx, deployment.ResourceGroup, deployment.NSGName, nil)
	if err != nil {
		return fmt.Errorf("failed to get NSG: %w", err)
	}

	// Find or create subnet
	subnet, actualVNetName, err := findOrCreateSubnet(client, *deployment, vnetName, subnetName)
	if err != nil {
		return fmt.Errorf("failed to find or create subnet: %w", err)
	}

	// Store the actual VNet and subnet names used
	deployment.VNetName = actualVNetName
	deployment.SubnetName = subnetName

	// Create VM (Azure will handle network creation automatically)
	vm := armcompute.VirtualMachine{
		Location: to.Ptr(deployment.Location),
		Properties: &armcompute.VirtualMachineProperties{
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: to.Ptr(armcompute.VirtualMachineSizeTypes(vmSize)),
			},
			StorageProfile: &armcompute.StorageProfile{
				OSDisk: &armcompute.OSDisk{
					CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesAttach),
					ManagedDisk: &armcompute.ManagedDiskParameters{
						ID: osDisk.ID,
					},
					OSType: to.Ptr(armcompute.OperatingSystemTypesLinux),
				},
				DataDisks: []*armcompute.DataDisk{
					{
						Lun:          to.Ptr(int32(0)),
						CreateOption: to.Ptr(armcompute.DiskCreateOptionTypesAttach),
						ManagedDisk: &armcompute.ManagedDiskParameters{
							ID: storageDisk.ID,
						},
					},
				},
			},
			NetworkProfile: &armcompute.NetworkProfile{
				NetworkAPIVersion: to.Ptr(armcompute.NetworkAPIVersionTwoThousandTwenty1101),
				NetworkInterfaceConfigurations: []*armcompute.VirtualMachineNetworkInterfaceConfiguration{
					{
						Name: to.Ptr(fmt.Sprintf("%s-nic", deployment.VMName)),
						Properties: &armcompute.VirtualMachineNetworkInterfaceConfigurationProperties{
							Primary: to.Ptr(true),
							IPConfigurations: []*armcompute.VirtualMachineNetworkInterfaceIPConfiguration{
								{
									Name: to.Ptr("ipconfig1"),
									Properties: &armcompute.VirtualMachineNetworkInterfaceIPConfigurationProperties{
										Subnet: &armcompute.SubResource{
											ID: subnet.ID,
										},
										PublicIPAddressConfiguration: &armcompute.VirtualMachinePublicIPAddressConfiguration{
											Name: to.Ptr(fmt.Sprintf("%s-ip", deployment.VMName)),
											Properties: &armcompute.VirtualMachinePublicIPAddressConfigurationProperties{
												PublicIPAllocationMethod: to.Ptr(armcompute.PublicIPAllocationMethodStatic),
											},
										},
									},
								},
							},
							NetworkSecurityGroup: &armcompute.SubResource{
								ID: nsg.ID,
							},
						},
					},
				},
			},
			SecurityProfile: &armcompute.SecurityProfile{
				UefiSettings: &armcompute.UefiSettings{
					SecureBootEnabled: to.Ptr(false),
					VTpmEnabled:       to.Ptr(true),
				},
				SecurityType: to.Ptr(armcompute.SecurityTypesConfidentialVM),
			},
			UserData: to.Ptr(base64.StdEncoding.EncodeToString([]byte(userData))),
		},
		Tags: map[string]*string{
			"Project": to.Ptr(projectTag),
			"VM":      to.Ptr(deployment.VMName),
		},
	}

	poller, err := client.vmClient.BeginCreateOrUpdate(
		client.ctx,
		deployment.ResourceGroup,
		deployment.VMName,
		vm,
		nil,
	)
	if err != nil {
		return err
	}

	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deleteCommand(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	deploymentID := args[0]

	// Load deployment info
	deployment, err := loadDeploymentInfo(deploymentID)
	if err != nil {
		return fmt.Errorf("failed to load deployment info: %w", err)
	}

	// Confirm deletion
	fmt.Printf("This will delete all resources for deployment '%s':\n", deploymentID)
	fmt.Printf("  - VM: %s\n", deployment.VMName)
	fmt.Printf("  - OS Disk: %s\n", deployment.OSDiskName)
	fmt.Printf("  - Storage Disk: %s\n", deployment.StorageDiskName)
	fmt.Printf("  - Network Security Group: %s\n", deployment.NSGName)
	if deployment.PublicIPName != "" {
		fmt.Printf("  - Public IP: %s\n", deployment.PublicIPName)
	}
	if deployment.NICName != "" {
		fmt.Printf("  - Network Interface: %s\n", deployment.NICName)
	}
	if deployment.AttestationName != "" {
		fmt.Printf("  - Attestation Provider: %s\n", deployment.AttestationName)
	}
	if deployment.SubnetName != "" {
		fmt.Printf("  - Subnet: %s\n", deployment.SubnetName)
	}
	if deployment.VNetName != "" {
		fmt.Printf("  - Virtual Network: %s\n", deployment.VNetName)
	}

	fmt.Print("\nAre you sure you want to continue? [y/N]: ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Deletion cancelled.")
		return nil
	}

	// Create Azure client
	client, err := createAzureClient(ctx, deployment.TenantID, deployment.SubscriptionID)
	if err != nil {
		return err
	}

	fmt.Println("\n🗑️  Deleting resources...")

	// Delete VM
	fmt.Println("  Deleting VM...")
	if err := deleteVM(client, deployment); err != nil {
		fmt.Printf("  ⚠️  Failed to delete VM: %v\n", err)
	}

	// Delete NIC
	if deployment.NICName != "" {
		fmt.Println("  Deleting Network Interface...")
		if err := deleteNIC(client, deployment); err != nil {
			fmt.Printf("  ⚠️  Failed to delete NIC: %v\n", err)
		}
	}

	// Delete Public IP
	if deployment.PublicIPName != "" {
		fmt.Println("  Deleting Public IP...")
		if err := deletePublicIP(client, deployment); err != nil {
			fmt.Printf("  ⚠️  Failed to delete Public IP: %v\n", err)
		}
	}

	// Delete NSG
	fmt.Println("  Deleting Network Security Group...")
	if err := deleteNSG(client, deployment); err != nil {
		fmt.Printf("  ⚠️  Failed to delete NSG: %v\n", err)
	}

	// Delete disks
	fmt.Println("  Deleting OS Disk...")
	if err := deleteDisk(client, deployment.ResourceGroup, deployment.OSDiskName); err != nil {
		fmt.Printf("  ⚠️  Failed to delete OS disk: %v\n", err)
	}

	fmt.Println("  Deleting Storage Disk...")
	if err := deleteDisk(client, deployment.ResourceGroup, deployment.StorageDiskName); err != nil {
		fmt.Printf("  ⚠️  Failed to delete storage disk: %v\n", err)
	}

	// Delete attestation provider
	if deployment.AttestationName != "" {
		fmt.Println("  Deleting Attestation Provider...")
		if err := deleteAttestationProvider(client, deployment); err != nil {
			fmt.Printf("  ⚠️  Failed to delete attestation provider: %v\n", err)
		}
	}

	// Delete subnet
	if deployment.SubnetName != "" {
		fmt.Println("  Deleting Subnet...")
		if err := deleteSubnet(client, deployment); err != nil {
			fmt.Printf("  ⚠️  Failed to delete subnet: %v\n", err)
		}
	}

	// Delete VNet
	if deployment.VNetName != "" {
		fmt.Println("  Deleting Virtual Network...")
		if err := deleteVNet(client, deployment); err != nil {
			fmt.Printf("  ⚠️  Failed to delete VNet: %v\n", err)
		}
	}

	// Remove deployment file
	deploymentFile := getDeploymentFile(deploymentID)
	os.Remove(deploymentFile)

	fmt.Println("\n✅ Deployment deleted successfully!")
	return nil
}

func deleteVM(client *AzureClient, deployment DeploymentInfo) error {
	poller, err := client.vmClient.BeginDelete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.VMName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deleteNIC(client *AzureClient, deployment DeploymentInfo) error {
	poller, err := client.nicClient.BeginDelete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.NICName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deletePublicIP(client *AzureClient, deployment DeploymentInfo) error {
	poller, err := client.publicIPClient.BeginDelete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.PublicIPName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deleteNSG(client *AzureClient, deployment DeploymentInfo) error {
	poller, err := client.networkClient.BeginDelete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.NSGName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deleteDisk(client *AzureClient, resourceGroup, diskName string) error {
	poller, err := client.computeClient.BeginDelete(
		client.ctx,
		resourceGroup,
		diskName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deleteAttestationProvider(client *AzureClient, deployment DeploymentInfo) error {
	resp, err := client.attestClient.Delete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.AttestationName,
		nil,
	)
	if err != nil {
		return err
	}
	_ = resp
	return nil
}

func deleteSubnet(client *AzureClient, deployment DeploymentInfo) error {
	if deployment.SubnetName == "" || deployment.VNetName == "" {
		return nil // Nothing to delete
	}

	poller, err := client.subnetClient.BeginDelete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.VNetName,
		deployment.SubnetName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func deleteVNet(client *AzureClient, deployment DeploymentInfo) error {
	if deployment.VNetName == "" {
		return nil // Nothing to delete
	}

	poller, err := client.vnetClient.BeginDelete(
		client.ctx,
		deployment.ResourceGroup,
		deployment.VNetName,
		nil,
	)
	if err != nil {
		return err
	}
	_, err = poller.PollUntilDone(client.ctx, nil)
	return err
}

func listCommand(cmd *cobra.Command, args []string) error {
	deploymentDir := getDeploymentDir()
	entries, err := os.ReadDir(deploymentDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No deployments found.")
			return nil
		}
		return err
	}

	fmt.Println("📋 SurgeTDX Deployments:")
	fmt.Println("─────────────────────────────────────────────────────────────")

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			deploymentID := strings.TrimSuffix(entry.Name(), ".json")
			deployment, err := loadDeploymentInfo(deploymentID)
			if err != nil {
				continue
			}

			fmt.Printf("ID: %-15s | VM: %-20s | RG: %-20s | Created: %s\n",
				deployment.ID,
				deployment.VMName,
				deployment.ResourceGroup,
				deployment.CreatedAt.Format("2006-01-02 15:04"),
			)
		}
	}

	return nil
}

func getDeploymentDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".surgetdx", "deployments")
}

func getDeploymentFile(deploymentID string) string {
	return filepath.Join(getDeploymentDir(), fmt.Sprintf("%s.json", deploymentID))
}

func saveDeploymentInfo(deployment DeploymentInfo) error {
	deploymentDir := getDeploymentDir()
	if err := os.MkdirAll(deploymentDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(deployment, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(getDeploymentFile(deployment.ID), data, 0644)
}

func loadDeploymentInfo(deploymentID string) (DeploymentInfo, error) {
	var deployment DeploymentInfo

	data, err := os.ReadFile(getDeploymentFile(deploymentID))
	if err != nil {
		return deployment, err
	}

	err = json.Unmarshal(data, &deployment)
	return deployment, err
}

func bytesToGB(bytes int64) int32 {
	return int32(math.Ceil(float64(bytes) / (1024 * 1024 * 1024)))
}

func updatePublicIPInfo(client *AzureClient, deployment *DeploymentInfo) error {
	// Get VM to find actual network interface
	vm, err := client.vmClient.Get(client.ctx, deployment.ResourceGroup, deployment.VMName, nil)
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Extract public IP name from VM's network interface
	if vm.Properties != nil && vm.Properties.NetworkProfile != nil {
		if len(vm.Properties.NetworkProfile.NetworkInterfaces) > 0 {
			nicID := vm.Properties.NetworkProfile.NetworkInterfaces[0].ID
			if nicID != nil {
				// Extract NIC name from resource ID
				parts := strings.Split(*nicID, "/")
				if len(parts) > 0 {
					nicName := parts[len(parts)-1]

					// Get NIC to find public IP
					nic, err := client.nicClient.Get(client.ctx, deployment.ResourceGroup, nicName, nil)
					if err == nil && nic.Properties != nil {
						if len(nic.Properties.IPConfigurations) > 0 {
							ipConfig := nic.Properties.IPConfigurations[0]
							if ipConfig.Properties != nil && ipConfig.Properties.PublicIPAddress != nil {
								pubIPID := ipConfig.Properties.PublicIPAddress.ID
								if pubIPID != nil {
									// Extract public IP name from resource ID
									parts := strings.Split(*pubIPID, "/")
									if len(parts) > 0 {
										deployment.PublicIPName = parts[len(parts)-1]
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return nil
}
