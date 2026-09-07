# Three-zone distributed chart profile.
# Keep monitoring nodes large enough for two replicas per zone.
min_node_count            = 1
max_node_count            = 2
machine_type              = "e2-medium"
monitoring_min_node_count = 3
monitoring_max_node_count = 8
monitoring_machine_type   = "e2-standard-4"
