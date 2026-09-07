-- The first-party CI fleet uses ordinary KVM guests on trusted hosts. This
-- class makes no SEV-SNP claim; native encrypted ZFS protects host storage.
-- Keep existing labels intact so their listeners and generations can drain.
INSERT INTO runner_classes (
    class, cpu_cores, memory_bytes, disk_bytes, tool_disk_bytes,
    process_disk_bytes, confidential_technology, guest_arch, workspace_epoch
) VALUES (
    'postflight-4vcpu-ubuntu24-turbo', 4, 17179869184, 85899345920,
    34359738368, 25769803776, '', 'x86_64', 'epoch-1'
);
