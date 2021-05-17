package yandex

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/hashicorp/go-multierror"
	"github.com/hashicorp/terraform-plugin-sdk/helper/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/terraform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yandex-cloud/go-genproto/yandex/cloud/mdb/kafka/v1"
)

const kfResource = "yandex_mdb_kafka_cluster.foo"

const kfVPCDependencies = `
resource "yandex_vpc_network" "mdb-kafka-test-net" {}

resource "yandex_vpc_subnet" "mdb-kafka-test-subnet-a" {
  zone           = "ru-central1-a"
  network_id     = yandex_vpc_network.mdb-kafka-test-net.id
  v4_cidr_blocks = ["10.1.0.0/24"]
}

resource "yandex_vpc_subnet" "mdb-kafka-test-subnet-b" {
  zone           = "ru-central1-b"
  network_id     = yandex_vpc_network.mdb-kafka-test-net.id
  v4_cidr_blocks = ["10.2.0.0/24"]
}

resource "yandex_vpc_subnet" "mdb-kafka-test-subnet-c" {
  zone           = "ru-central1-c"
  network_id     = yandex_vpc_network.mdb-kafka-test-net.id
  v4_cidr_blocks = ["10.3.0.0/24"]
}

resource "yandex_vpc_security_group" "mdb-kafka-test-sg-x" {
  network_id     = "${yandex_vpc_network.mdb-kafka-test-net.id}"
  ingress {
    protocol          = "ANY"
    description       = "Allow incoming traffic from members of the same security group"
    from_port         = 0
    to_port           = 65535
    v4_cidr_blocks    = ["0.0.0.0/0"]
  }
  egress {
    protocol          = "ANY"
    description       = "Allow outgoing traffic to members of the same security group"
    from_port         = 0
    to_port           = 65535
    v4_cidr_blocks    = ["0.0.0.0/0"]
  }
}

resource "yandex_vpc_security_group" "mdb-kafka-test-sg-y" {
  network_id     = "${yandex_vpc_network.mdb-kafka-test-net.id}"

  ingress {
    protocol          = "ANY"
    description       = "Allow incoming traffic from members of the same security group"
    from_port         = 0
    to_port           = 65535
    v4_cidr_blocks    = ["0.0.0.0/0"]
  }
  egress {
    protocol          = "ANY"
    description       = "Allow outgoing traffic to members of the same security group"
    from_port         = 0
    to_port           = 65535
    v4_cidr_blocks    = ["0.0.0.0/0"]
  }
}
`

func init() {
	resource.AddTestSweepers("yandex_mdb_kafka_cluster", &resource.Sweeper{
		Name: "yandex_mdb_kafka_cluster",
		F:    testSweepMDBKafkaCluster,
	})
}

func testSweepMDBKafkaCluster(_ string) error {
	conf, err := configForSweepers()
	if err != nil {
		return fmt.Errorf("error getting client: %s", err)
	}

	resp, err := conf.sdk.MDB().Kafka().Cluster().List(conf.Context(), &kafka.ListClustersRequest{
		FolderId: conf.FolderID,
		PageSize: defaultMDBPageSize,
	})
	if err != nil {
		return fmt.Errorf("error getting Kafka clusters: %s", err)
	}

	result := &multierror.Error{}
	for _, c := range resp.Clusters {
		if !sweepMDBKafkaCluster(conf, c.Id) {
			result = multierror.Append(result, fmt.Errorf("failed to sweep Kafka cluster %q", c.Id))
		}
	}

	return result.ErrorOrNil()
}

func sweepMDBKafkaCluster(conf *Config, id string) bool {
	return sweepWithRetry(sweepMDBKafkaClusterOnce, conf, "Kafka cluster", id)
}

func sweepMDBKafkaClusterOnce(conf *Config, id string) error {
	ctx, cancel := conf.ContextWithTimeout(yandexMDBKafkaClusterDeleteTimeout)
	defer cancel()

	op, err := conf.sdk.MDB().Kafka().Cluster().Delete(ctx, &kafka.DeleteClusterRequest{
		ClusterId: id,
	})
	return handleSweepOperation(ctx, conf, op, err)
}

func mdbKafkaClusterImportStep(name string) resource.TestStep {
	return resource.TestStep{
		ResourceName:      name,
		ImportState:       true,
		ImportStateVerify: true,
		ImportStateVerifyIgnore: []string{
			"user",       // passwords are not returned
			"topic",      // order may differs
			"subnet_ids", // subnets not returned
			"health",     // volatile value
		},
	}
}

func TestExpandKafkaClusterConfig(t *testing.T) {
	raw := map[string]interface{}{
		"folder_id":   "",
		"name":        "kafka-tf-name",
		"description": "kafka-tf-desc",
		"environment": "PRESTABLE",
		"labels":      map[string]interface{}{"label1": "val1", "label2": "val2"},
		"config": []interface{}{
			map[string]interface{}{
				"version":       "2.6",
				"brokers_count": 1,
				"zones":         []interface{}{"ru-central1-b", "ru-central1-c"},
				"kafka": []interface{}{
					map[string]interface{}{
						"resources": []interface{}{
							map[string]interface{}{
								"resource_preset_id": "s2.micro",
								"disk_size":          20,
								"disk_type_id":       "network-ssd",
							},
						},
						"kafka_config": []interface{}{
							map[string]interface{}{
								"compression_type":    "COMPRESSION_TYPE_ZSTD",
								"log_retention_bytes": 1024 * 1024 * 1024,
								"log_preallocate":     true,
							},
						},
					},
				},
				"zookeeper": []interface{}{
					map[string]interface{}{
						"resources": []interface{}{
							map[string]interface{}{
								"resource_preset_id": "b2.medium",
								"disk_size":          32,
								"disk_type_id":       "network-ssd",
							},
						},
					},
				},
			},
		},
		"subnet_ids":         []interface{}{"rc1a-subnet", "rc1b-subnet", "rc1c-subnet"},
		"security_group_ids": []interface{}{"security-group-x", "security-group-y"},
		"topic": []interface{}{
			map[string]interface{}{
				"name":               "raw_events",
				"partitions":         12,
				"replication_factor": 1,
				"topic_config": []interface{}{
					map[string]interface{}{
						"cleanup_policy":      "CLEANUP_POLICY_COMPACT_AND_DELETE",
						"min_insync_replicas": 1,
						"max_message_bytes":   16777216,
					},
				},
			},
			map[string]interface{}{
				"name":               "final",
				"partitions":         13,
				"replication_factor": 2,
			},
		},
		"user": []interface{}{
			map[string]interface{}{
				"name":     "alice",
				"password": "password",
				"permission": []interface{}{
					map[string]interface{}{
						"topic_name": "raw_events",
						"role":       "ACCESS_ROLE_PRODUCER",
					},
				},
			},
			map[string]interface{}{
				"name":     "bob",
				"password": "password",
				"permission": []interface{}{
					map[string]interface{}{
						"topic_name": "raw_events",
						"role":       "ACCESS_ROLE_CONSUMER",
					},
					map[string]interface{}{
						"topic_name": "final",
						"role":       "ACCESS_ROLE_PRODUCER",
					},
				},
			},
		},
	}
	resourceData := schema.TestResourceDataRaw(t, resourceYandexMDBKafkaCluster().Schema, raw)

	config := &Config{FolderID: "folder-777"}
	req, err := prepareKafkaCreateRequest(resourceData, config)
	if err != nil {
		require.NoError(t, err)
	}

	expected := &kafka.CreateClusterRequest{
		FolderId:    "folder-777",
		Name:        "kafka-tf-name",
		Description: "kafka-tf-desc",
		Labels:      map[string]string{"label1": "val1", "label2": "val2"},
		Environment: kafka.Cluster_PRESTABLE,
		ConfigSpec: &kafka.ConfigSpec{
			Version:      "2.6",
			BrokersCount: &wrappers.Int64Value{Value: int64(1)},
			ZoneId:       []string{"ru-central1-b", "ru-central1-c"},
			Kafka: &kafka.ConfigSpec_Kafka{
				Resources: &kafka.Resources{
					ResourcePresetId: "s2.micro",
					DiskSize:         21474836480,
					DiskTypeId:       "network-ssd",
				},
				KafkaConfig: &kafka.ConfigSpec_Kafka_KafkaConfig_2_6{
					KafkaConfig_2_6: &kafka.KafkaConfig2_6{
						CompressionType:   kafka.CompressionType_COMPRESSION_TYPE_ZSTD,
						LogRetentionBytes: &wrappers.Int64Value{Value: 1024 * 1024 * 1024},
						LogPreallocate:    &wrappers.BoolValue{Value: true},
					},
				},
			},
			Zookeeper: &kafka.ConfigSpec_Zookeeper{
				Resources: &kafka.Resources{
					ResourcePresetId: "b2.medium",
					DiskSize:         34359738368,
					DiskTypeId:       "network-ssd",
				},
			},
		},
		SubnetId: []string{"rc1a-subnet", "rc1b-subnet", "rc1c-subnet"},
		TopicSpecs: []*kafka.TopicSpec{
			{
				Name:              "raw_events",
				Partitions:        &wrappers.Int64Value{Value: int64(12)},
				ReplicationFactor: &wrappers.Int64Value{Value: int64(1)},
				TopicConfig: &kafka.TopicSpec_TopicConfig_2_6{
					TopicConfig_2_6: &kafka.TopicConfig2_6{
						CleanupPolicy:     kafka.TopicConfig2_6_CLEANUP_POLICY_COMPACT_AND_DELETE,
						MinInsyncReplicas: &wrappers.Int64Value{Value: int64(1)},
						MaxMessageBytes:   &wrappers.Int64Value{Value: int64(16777216)},
					},
				},
			},
			{
				Name:              "final",
				Partitions:        &wrappers.Int64Value{Value: int64(13)},
				ReplicationFactor: &wrappers.Int64Value{Value: int64(2)},
			},
		},
		UserSpecs: []*kafka.UserSpec{
			{
				Name:     "bob",
				Password: "password",
				Permissions: []*kafka.Permission{
					{
						TopicName: "final",
						Role:      kafka.Permission_ACCESS_ROLE_PRODUCER,
					},
					{
						TopicName: "raw_events",
						Role:      kafka.Permission_ACCESS_ROLE_CONSUMER,
					},
				},
			},
			{
				Name:     "alice",
				Password: "password",
				Permissions: []*kafka.Permission{
					{
						TopicName: "raw_events",
						Role:      kafka.Permission_ACCESS_ROLE_PRODUCER,
					},
				},
			},
		},
		SecurityGroupIds: []string{"security-group-x", "security-group-y"},
	}

	assert.Equal(t, expected, req)
}

// Test that a Kafka Cluster can be created, updated and destroyed in single zone mode
func TestAccMDBKafkaCluster_single(t *testing.T) {
	t.Parallel()

	var r kafka.Cluster
	kfName := acctest.RandomWithPrefix("tf-kafka")
	kfDesc := "Kafka Cluster Terraform Test"
	kfDescUpdated := "Kafka Cluster Terraform Test (updated)"
	folderID := getExampleFolderID()

	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckMDBKafkaClusterDestroy,
		Steps: []resource.TestStep{
			// Create Kafka Cluster
			{
				Config: testAccMDBKafkaClusterConfigMain(kfName, kfDesc),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckMDBKafkaClusterExists(kfResource, &r, 1),
					resource.TestCheckResourceAttr(kfResource, "name", kfName),
					resource.TestCheckResourceAttr(kfResource, "folder_id", folderID),
					resource.TestCheckResourceAttr(kfResource, "description", kfDesc),
					resource.TestCheckResourceAttr(kfResource, "security_group_ids.#", "1"),
					testAccCheckMDBKafkaClusterContainsLabel(&r, "test_key", "test_value"),
					testAccCheckMDBKafkaConfigKafkaHasResources(&r, "s2.micro", "network-hdd", 16*1024*1024*1024),
					testAccCheckMDBKafkaClusterHasTopics(kfResource, []string{"raw_events", "final"}),
					testAccCheckMDBKafkaClusterHasUsers(kfResource, map[string][]string{"alice": {"raw_events"}, "bob": {"raw_events", "final"}}),
					testAccCheckMDBKafkaClusterCompressionType(&r, kafka.CompressionType_COMPRESSION_TYPE_ZSTD),
					testAccCheckMDBKafkaClusterLogRetentionBytes(&r, 1073741824),
					testAccCheckMDBKafkaTopicMaxMessageBytes(kfResource, "raw_events", 16777216),
					testAccCheckMDBKafkaTopicConfig(kfResource, "raw_events", &kafka.TopicConfig2_6{CleanupPolicy: kafka.TopicConfig2_6_CLEANUP_POLICY_COMPACT_AND_DELETE, MaxMessageBytes: &wrappers.Int64Value{Value: 16777216}, SegmentBytes: &wrappers.Int64Value{Value: 134217728}}),
					testAccCheckMDBKafkaClusterLogPreallocate(&r, true),
					testAccCheckCreatedAtAttr(kfResource),
				),
			},
			mdbKafkaClusterImportStep(kfResource),
			// Change some options
			{
				Config: testAccMDBKafkaClusterConfigUpdated(kfName, kfDescUpdated),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckMDBKafkaClusterExists(kfResource, &r, 1),
					resource.TestCheckResourceAttr(kfResource, "name", kfName),
					resource.TestCheckResourceAttr(kfResource, "folder_id", folderID),
					resource.TestCheckResourceAttr(kfResource, "description", kfDescUpdated),
					resource.TestCheckResourceAttr(kfResource, "security_group_ids.#", "2"),
					testAccCheckMDBKafkaClusterContainsLabel(&r, "new_key", "new_value"),
					testAccCheckMDBKafkaConfigKafkaHasResources(&r, "s2.medium", "network-hdd", 17*1024*1024*1024),
					testAccCheckMDBKafkaClusterHasTopics(kfResource, []string{"raw_events", "new_topic"}),
					testAccCheckMDBKafkaClusterHasUsers(kfResource, map[string][]string{"alice": {"raw_events", "raw_events"}, "charlie": {"raw_events", "new_topic"}}),
					testAccCheckMDBKafkaClusterCompressionType(&r, kafka.CompressionType_COMPRESSION_TYPE_ZSTD),
					testAccCheckMDBKafkaClusterLogRetentionBytes(&r, 2147483648),
					testAccCheckMDBKafkaClusterLogSegmentBytes(&r, 268435456),
					testAccCheckMDBKafkaClusterLogPreallocate(&r, true),
					testAccCheckMDBKafkaTopicConfig(kfResource, "raw_events", &kafka.TopicConfig2_6{CleanupPolicy: kafka.TopicConfig2_6_CLEANUP_POLICY_DELETE, MaxMessageBytes: &wrappers.Int64Value{Value: 33554432}, SegmentBytes: &wrappers.Int64Value{Value: 268435456}}),
					testAccCheckCreatedAtAttr(kfResource),
				),
			},
		},
	})
}

// Test that a Kafka Cluster can be created, updated and destroyed in high availability mode
func TestAccMDBKafkaCluster_HA(t *testing.T) {
	t.Parallel()

	var r kafka.Cluster
	kfName := acctest.RandomWithPrefix("tf-kafka")
	kfDesc := "Kafka Cluster Terraform Test"
	kfDescUpdated := "Kafka Cluster Terraform Test (updated)"
	folderID := getExampleFolderID()

	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccCheckMDBKafkaClusterDestroy,
		Steps: []resource.TestStep{
			// Create Kafka Cluster
			{
				Config: testAccMDBKafkaClusterConfigMainHA(kfName, kfDesc),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckMDBKafkaClusterExists(kfResource, &r, 1),
					resource.TestCheckResourceAttr(kfResource, "name", kfName),
					resource.TestCheckResourceAttr(kfResource, "folder_id", folderID),
					resource.TestCheckResourceAttr(kfResource, "description", kfDesc),
					resource.TestCheckResourceAttr(kfResource, "security_group_ids.#", "1"),
					testAccCheckMDBKafkaClusterContainsLabel(&r, "test_key", "test_value"),
					testAccCheckMDBKafkaConfigKafkaHasResources(&r, "s2.micro", "network-hdd", 17179869184),
					testAccCheckMDBKafkaConfigZookeeperHasResources(&r, "s2.micro", "network-ssd", 17179869184),
					testAccCheckMDBKafkaClusterHasTopics(kfResource, []string{"raw_events", "final"}),
					testAccCheckMDBKafkaClusterHasUsers(kfResource, map[string][]string{"alice": {"raw_events"}, "bob": {"raw_events", "final"}}),
					testAccCheckMDBKafkaConfigZones(&r, []string{"ru-central1-a", "ru-central1-b"}),
					testAccCheckMDBKafkaConfigBrokersCount(&r, 1),
					testAccCheckMDBKafkaClusterCompressionType(&r, kafka.CompressionType_COMPRESSION_TYPE_ZSTD),
					testAccCheckMDBKafkaClusterLogRetentionBytes(&r, 1073741824),
					testAccCheckMDBKafkaTopicConfig(kfResource, "raw_events", &kafka.TopicConfig2_6{MaxMessageBytes: &wrappers.Int64Value{Value: 16777216}, SegmentBytes: &wrappers.Int64Value{Value: 134217728}, Preallocate: &wrappers.BoolValue{Value: true}}),
					testAccCheckMDBKafkaClusterLogPreallocate(&r, true),
					testAccCheckCreatedAtAttr(kfResource),
				),
			},
			mdbKafkaClusterImportStep(kfResource),
			// Change some options
			{
				Config: testAccMDBKafkaClusterConfigUpdatedHA(kfName, kfDescUpdated),
				Check: resource.ComposeTestCheckFunc(
					testAccCheckMDBKafkaClusterExists(kfResource, &r, 1),
					resource.TestCheckResourceAttr(kfResource, "name", kfName),
					resource.TestCheckResourceAttr(kfResource, "folder_id", folderID),
					resource.TestCheckResourceAttr(kfResource, "description", kfDescUpdated),
					resource.TestCheckResourceAttr(kfResource, "security_group_ids.#", "2"),
					testAccCheckMDBKafkaClusterContainsLabel(&r, "new_key", "new_value"),
					testAccCheckMDBKafkaConfigKafkaHasResources(&r, "s2.micro", "network-hdd", 19327352832),
					testAccCheckMDBKafkaConfigZookeeperHasResources(&r, "s2.medium", "network-ssd", 19327352832),
					testAccCheckMDBKafkaConfigZones(&r, []string{"ru-central1-a", "ru-central1-b", "ru-central1-c"}),
					testAccCheckMDBKafkaConfigBrokersCount(&r, 2),
					testAccCheckMDBKafkaClusterHasTopics(kfResource, []string{"raw_events", "new_topic"}),
					testAccCheckMDBKafkaClusterHasUsers(kfResource, map[string][]string{"alice": {"raw_events"}, "charlie": {"raw_events", "new_topic"}}),
					testAccCheckMDBKafkaClusterCompressionType(&r, kafka.CompressionType_COMPRESSION_TYPE_ZSTD),
					testAccCheckMDBKafkaClusterLogRetentionBytes(&r, 2147483648),
					testAccCheckMDBKafkaClusterLogSegmentBytes(&r, 268435456),
					testAccCheckMDBKafkaTopicConfig(kfResource, "raw_events", &kafka.TopicConfig2_6{MaxMessageBytes: &wrappers.Int64Value{Value: 33554432}, SegmentBytes: &wrappers.Int64Value{Value: 268435456}, RetentionBytes: &wrappers.Int64Value{Value: 1073741824}}),
					testAccCheckMDBKafkaClusterLogPreallocate(&r, true),
					testAccCheckCreatedAtAttr(kfResource),
				),
			},
		},
	})
}

// Test that a Kafka Cluster can be created, updated and destroyed in high availability configuration
func testAccCheckMDBKafkaClusterExists(n string, r *kafka.Cluster, hosts int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[n]
		if !ok {
			return fmt.Errorf("not found: %s", n)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no ID is set")
		}

		config := testAccProvider.Meta().(*Config)

		found, err := config.sdk.MDB().Kafka().Cluster().Get(context.Background(), &kafka.GetClusterRequest{
			ClusterId: rs.Primary.ID,
		})
		if err != nil {
			return err
		}

		if found.Id != rs.Primary.ID {
			return fmt.Errorf("Kafka Cluster not found")
		}

		*r = *found
		return nil
	}
}

func testAccCheckMDBKafkaClusterDestroy(s *terraform.State) error {
	config := testAccProvider.Meta().(*Config)

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "yandex_mdb_kafka_cluster" {
			continue
		}

		_, err := config.sdk.MDB().Kafka().Cluster().Get(context.Background(), &kafka.GetClusterRequest{
			ClusterId: rs.Primary.ID,
		})

		if err == nil {
			return fmt.Errorf("kafka Cluster still exists")
		}
	}

	return nil
}

func testAccMDBKafkaClusterConfigMain(name, desc string) string {
	return fmt.Sprintf(kfVPCDependencies+`
resource "yandex_mdb_kafka_cluster" "foo" {
	name        = "%s"
	description = "%s"
	environment = "PRESTABLE"
	network_id  = yandex_vpc_network.mdb-kafka-test-net.id
	labels = {
	  test_key = "test_value"
	}
	subnet_ids = [yandex_vpc_subnet.mdb-kafka-test-subnet-a.id]
	security_group_ids = [yandex_vpc_security_group.mdb-kafka-test-sg-x.id]

	config {
	  version          = "2.6"
	  brokers_count    = 1
	  zones            = ["ru-central1-a"]
	  assign_public_ip = false
	  unmanaged_topics = false
	  kafka {
		resources {
		  resource_preset_id = "s2.micro"
		  disk_type_id       = "network-hdd"
		  disk_size          = 16
		}

		kafka_config {
		  compression_type    = "COMPRESSION_TYPE_ZSTD"
		  log_retention_bytes = 1073741824
		  log_preallocate     = true
		}
	  }
	}

	topic {
	  name               = "raw_events"
	  partitions         = 1
	  replication_factor = 1
	  topic_config {
		cleanup_policy    = "CLEANUP_POLICY_COMPACT_AND_DELETE"
		max_message_bytes = 16777216
		segment_bytes     = 134217728
	  }
	}

	topic {
	  name               = "final"
	  partitions         = 2
	  replication_factor = 1
	  topic_config {
		compression_type = "COMPRESSION_TYPE_ZSTD"
		segment_bytes    = 134217728
	  }
	}

	user {
	  name     = "alice"
	  password = "password"
	  permission {
		topic_name = "raw_events"
		role       = "ACCESS_ROLE_PRODUCER"
	  }
	}

	user {
	  name     = "bob"
	  password = "password"
	  permission {
		topic_name = "raw_events"
		role       = "ACCESS_ROLE_CONSUMER"
	  }
	  permission {
		topic_name = "final"
		role       = "ACCESS_ROLE_PRODUCER"
	  }
	}
}
`, name, desc)
}

func testAccMDBKafkaClusterConfigUpdated(name, desc string) string {
	return fmt.Sprintf(kfVPCDependencies+`
resource "yandex_mdb_kafka_cluster" "foo" {
	name        = "%s"
	description = "%s"
	environment = "PRESTABLE"
	network_id  = yandex_vpc_network.mdb-kafka-test-net.id
	labels = {
		test_key = "test_value"
		new_key = "new_value"
	}
	subnet_ids = [yandex_vpc_subnet.mdb-kafka-test-subnet-a.id]
	security_group_ids = [yandex_vpc_security_group.mdb-kafka-test-sg-x.id, yandex_vpc_security_group.mdb-kafka-test-sg-y.id]

	config {
		version = "2.6"
		brokers_count = 1
		zones = ["ru-central1-a"]
		assign_public_ip = false
		unmanaged_topics = false
		kafka {
			resources {
				resource_preset_id = "s2.medium"
				disk_type_id       = "network-hdd"
			  	disk_size          = 17
			}
			kafka_config {
				compression_type    = "COMPRESSION_TYPE_ZSTD"
				log_retention_bytes = 2147483648
				log_segment_bytes   = 268435456
				log_preallocate     = true
			}
		}
	}

	topic {
		name = "raw_events"
		partitions = 1
		replication_factor = 1

		topic_config {
			cleanup_policy = "CLEANUP_POLICY_DELETE"
	 		max_message_bytes = 33554432
			segment_bytes = 268435456
		}
	}

	topic {
		name = "new_topic"
		partitions = 1
		replication_factor = 1
	}

	user {
		name = "alice"
		password = "password"
		permission {
			topic_name = "raw_events"
			role = "ACCESS_ROLE_PRODUCER"
		}
		permission {
			topic_name = "raw_events"
			role = "ACCESS_ROLE_CONSUMER"
		}
	}

	user {
		name = "charlie"
		password = "password"
		permission {
			topic_name = "raw_events"
			role = "ACCESS_ROLE_CONSUMER"
		}
		permission {
			topic_name = "new_topic"
			role = "ACCESS_ROLE_PRODUCER"
		}
	}
}
`, name, desc)
}

func testAccCheckMDBKafkaClusterContainsLabel(r *kafka.Cluster, key string, value string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		v, ok := r.Labels[key]
		if !ok {
			return fmt.Errorf("expected label with key '%s' not found", key)
		}
		if v != value {
			return fmt.Errorf("incorrect label value for key '%s': expected '%s' but found '%s'", key, value, v)
		}
		return nil
	}
}

func testAccCheckMDBKafkaClusterCompressionType(r *kafka.Cluster, value kafka.CompressionType) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		v := r.Config.Kafka.GetKafkaConfig_2_6().CompressionType
		if v != value {
			return fmt.Errorf("incorrect compression_type value: expected '%s' but found '%s'", value, v)
		}
		return nil
	}
}

func testAccCheckMDBKafkaClusterLogRetentionBytes(r *kafka.Cluster, value int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		v := r.Config.Kafka.GetKafkaConfig_2_6().LogRetentionBytes
		if v.GetValue() != value {
			return fmt.Errorf("incorrect log_retention_bytes value: expected '%v' but found '%v'", value, v.GetValue())
		}
		return nil
	}
}

func testAccCheckMDBKafkaClusterLogSegmentBytes(r *kafka.Cluster, value int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		v := r.Config.Kafka.GetKafkaConfig_2_6().LogSegmentBytes
		if v.GetValue() != value {
			return fmt.Errorf("incorrect log_segment_bytes value: expected '%v' but found '%v'", value, v.GetValue())
		}
		return nil
	}
}

func testAccCheckMDBKafkaClusterLogPreallocate(r *kafka.Cluster, value bool) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		v := r.Config.Kafka.GetKafkaConfig_2_6().LogPreallocate
		if v.GetValue() != value {
			return fmt.Errorf("incorrect log_preallocate value: expected '%v' but found '%v'", value, v.GetValue())
		}
		return nil
	}
}

func testAccCheckMDBKafkaConfigKafkaHasResources(r *kafka.Cluster, resourcePresetID string, diskType string, diskSize int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := r.Config.Kafka.Resources
		if rs.ResourcePresetId != resourcePresetID {
			return fmt.Errorf("expected resource preset id '%s', got '%s'", resourcePresetID, rs.ResourcePresetId)
		}
		if rs.DiskTypeId != diskType {
			return fmt.Errorf("expected disk type '%s', got '%s'", diskType, rs.DiskTypeId)
		}
		if rs.DiskSize != diskSize {
			return fmt.Errorf("expected disk size '%d', got '%d'", diskSize, rs.DiskSize)
		}
		return nil
	}
}

func testAccCheckMDBKafkaConfigBrokersCount(r *kafka.Cluster, brokers int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if r.Config.BrokersCount.GetValue() != brokers {
			return fmt.Errorf("expected brokers '%v', got '%v'", brokers, r.Config.BrokersCount.GetValue())
		}
		return nil
	}
}

func testAccCheckMDBKafkaConfigZones(r *kafka.Cluster, zones []string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		if !reflect.DeepEqual(r.Config.ZoneId, zones) {
			return fmt.Errorf("expected zones '%s', got '%s'", zones, r.Config.ZoneId)
		}
		return nil
	}
}

func testAccCheckMDBKafkaConfigZookeeperHasResources(r *kafka.Cluster, resourcePresetID string, diskType string, diskSize int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := r.Config.Zookeeper.Resources
		if rs.ResourcePresetId != resourcePresetID {
			return fmt.Errorf("expected resource preset id '%s', got '%s'", resourcePresetID, rs.ResourcePresetId)
		}
		if rs.DiskTypeId != diskType {
			return fmt.Errorf("expected disk type '%s', got '%s'", diskType, rs.DiskTypeId)
		}
		if rs.DiskSize != diskSize {
			return fmt.Errorf("expected disk size '%d', got '%d'", diskSize, rs.DiskSize)
		}
		return nil
	}
}

func testAccCheckMDBKafkaClusterHasTopics(r string, topics []string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[r]
		if !ok {
			return fmt.Errorf("not found: %s", r)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no ID is set")
		}

		config := testAccProvider.Meta().(*Config)

		resp, err := config.sdk.MDB().Kafka().Topic().List(context.Background(), &kafka.ListTopicsRequest{
			ClusterId: rs.Primary.ID,
			PageSize:  defaultMDBPageSize,
		})
		if err != nil {
			return err
		}
		tpcs := []string{}
		for _, d := range resp.Topics {
			tpcs = append(tpcs, d.Name)
		}

		if len(tpcs) != len(topics) {
			return fmt.Errorf("expected topics %v, found %v", topics, tpcs)
		}

		sort.Strings(tpcs)
		sort.Strings(topics)
		if fmt.Sprintf("%v", tpcs) != fmt.Sprintf("%v", topics) {
			return fmt.Errorf("cluster has wrong topics, %v. Expected %v", tpcs, topics)
		}

		return nil
	}
}

func testAccCheckMDBKafkaTopicMaxMessageBytes(r string, topic string, value int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[r]
		if !ok {
			return fmt.Errorf("not found: %s", r)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no ID is set")
		}

		config := testAccProvider.Meta().(*Config)

		resp, err := config.sdk.MDB().Kafka().Topic().Get(context.Background(), &kafka.GetTopicRequest{
			ClusterId: rs.Primary.ID,
			TopicName: topic,
		})
		if err != nil {
			return err
		}
		v := resp.GetTopicConfig_2_6().MaxMessageBytes.GetValue()
		if v != value {
			return fmt.Errorf("MaxMessageByte for topic %v has value: %v, expected: %v", topic, v, value)
		}
		return nil
	}
}

func testAccCheckMDBKafkaTopicConfig(r string, topicName string, topicConfig *kafka.TopicConfig2_6) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[r]
		if !ok {
			return fmt.Errorf("not found: %s", r)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no ID is set")
		}

		config := testAccProvider.Meta().(*Config)

		resp, err := config.sdk.MDB().Kafka().Topic().Get(context.Background(), &kafka.GetTopicRequest{
			ClusterId: rs.Primary.ID,
			TopicName: topicName,
		})
		if err != nil {
			return err
		}
		actualTopicConfig := resp.GetTopicConfig_2_6()
		if !reflect.DeepEqual(topicConfig, actualTopicConfig) {
			return fmt.Errorf("topic %v differs, actual: %v, expected %v", topicName, actualTopicConfig, topicConfig)
		}
		return nil
	}
}

func testAccCheckMDBKafkaClusterHasUsers(r string, perms map[string][]string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[r]
		if !ok {
			return fmt.Errorf("not found: %s", r)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("no ID is set")
		}

		config := testAccProvider.Meta().(*Config)

		resp, err := config.sdk.MDB().Kafka().User().List(context.Background(), &kafka.ListUsersRequest{
			ClusterId: rs.Primary.ID,
			PageSize:  defaultMDBPageSize,
		})
		if err != nil {
			return err
		}
		users := resp.Users

		if len(users) != len(perms) {
			return fmt.Errorf("expected %d users, found %d", len(perms), len(users))
		}

		for _, u := range users {
			ps, ok := perms[u.Name]
			if !ok {
				return fmt.Errorf("unexpected user: %s", u.Name)
			}

			ups := []string{}
			for _, p := range u.Permissions {
				ups = append(ups, p.TopicName)
			}

			sort.Strings(ps)
			sort.Strings(ups)
			if fmt.Sprintf("%v", ps) != fmt.Sprintf("%v", ups) {
				return fmt.Errorf("user %s has wrong permissions, %v. Expected %v", u.Name, ups, ps)
			}
		}

		return nil
	}
}

func testAccMDBKafkaClusterConfigMainHA(name, desc string) string {
	return fmt.Sprintf(kfVPCDependencies+`
resource "yandex_mdb_kafka_cluster" "foo" {
	name        = "%s"
	description = "%s"
	environment = "PRODUCTION"
	network_id  = yandex_vpc_network.mdb-kafka-test-net.id
	labels = {
	  test_key = "test_value"
	}
	subnet_ids = [
	  yandex_vpc_subnet.mdb-kafka-test-subnet-a.id,
	  yandex_vpc_subnet.mdb-kafka-test-subnet-b.id,
	  yandex_vpc_subnet.mdb-kafka-test-subnet-c.id
	]
	security_group_ids = [yandex_vpc_security_group.mdb-kafka-test-sg-x.id]

	config {
	  version          = "2.6"
	  brokers_count    = 1
	  zones            = ["ru-central1-a", "ru-central1-b"]
	  assign_public_ip = false
	  unmanaged_topics = false
	  kafka {
		resources {
		  resource_preset_id = "s2.micro"
		  disk_type_id       = "network-hdd"
		  disk_size          = 16
		}
		kafka_config {
		  compression_type    = "COMPRESSION_TYPE_ZSTD"
		  log_retention_bytes = 1073741824
		  log_preallocate     = true
		}
	  }
	  zookeeper {
		resources {
		  resource_preset_id = "s2.micro"
		  disk_type_id       = "network-ssd"
		  disk_size          = 16
		}
	  }
	}

	topic {
	  name               = "raw_events"
	  partitions         = 1
	  replication_factor = 1

	  topic_config {
		max_message_bytes = 16777216
		segment_bytes     = 134217728
		preallocate       = true
	  }
	}

	topic {
	  name               = "final"
	  partitions         = 2
	  replication_factor = 1
	}

	user {
	  name     = "alice"
	  password = "password"
	  permission {
		topic_name = "raw_events"
		role       = "ACCESS_ROLE_PRODUCER"
	  }
	}

	user {
	  name     = "bob"
	  password = "password"
	  permission {
		topic_name = "raw_events"
		role       = "ACCESS_ROLE_CONSUMER"
	  }
	  permission {
		topic_name = "final"
		role       = "ACCESS_ROLE_PRODUCER"
	  }
	}
}
`, name, desc)
}

func testAccMDBKafkaClusterConfigUpdatedHA(name, desc string) string {
	return fmt.Sprintf(kfVPCDependencies+`
resource "yandex_mdb_kafka_cluster" "foo" {
	name        = "%s"
	description = "%s"
	environment = "PRODUCTION"
	network_id  = yandex_vpc_network.mdb-kafka-test-net.id
	labels = {
	  test_key = "test_value"
	  new_key  = "new_value"
	}
	subnet_ids = [
	  yandex_vpc_subnet.mdb-kafka-test-subnet-a.id,
	  yandex_vpc_subnet.mdb-kafka-test-subnet-b.id,
	  yandex_vpc_subnet.mdb-kafka-test-subnet-c.id
	]
	security_group_ids = [
	  yandex_vpc_security_group.mdb-kafka-test-sg-x.id,
	  yandex_vpc_security_group.mdb-kafka-test-sg-y.id
	]

	config {
	  version          = "2.6"
	  brokers_count    = 2
	  zones            = ["ru-central1-a", "ru-central1-b", "ru-central1-c"]
	  assign_public_ip = false
	  unmanaged_topics = false
	  kafka {
		resources {
		  resource_preset_id = "s2.micro"
		  disk_type_id       = "network-hdd"
		  disk_size          = 18
		}
		kafka_config {
		  compression_type    = "COMPRESSION_TYPE_ZSTD"
		  log_retention_bytes = 2147483648
		  log_segment_bytes   = 268435456
		  log_preallocate     = true
		}
	  }
	  zookeeper {
		resources {
		  resource_preset_id = "s2.medium"
		  disk_type_id       = "network-ssd"
		  disk_size          = 18
		}
	  }
	}

	topic {
	  name               = "raw_events"
	  partitions         = 2
	  replication_factor = 1
	  topic_config {
		max_message_bytes = 33554432
		segment_bytes     = 268435456
		preallocate       = false
		retention_bytes   = 1073741824
	  }
	}

	topic {
	  name               = "new_topic"
	  partitions         = 1
	  replication_factor = 1
	}

	user {
	  name     = "alice"
	  password = "password"
	  permission {
		topic_name = "raw_events"
		role       = "ACCESS_ROLE_PRODUCER"
	  }
	}

	user {
	  name     = "charlie"
	  password = "password"
	  permission {
		topic_name = "raw_events"
		role       = "ACCESS_ROLE_CONSUMER"
	  }
	  permission {
		topic_name = "new_topic"
		role       = "ACCESS_ROLE_PRODUCER"
	  }
	}
}
`, name, desc)
}
