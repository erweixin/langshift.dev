#!/usr/bin/env node
import { readFile } from "node:fs/promises";
import path from "node:path";

const root = path.resolve(process.argv[2] ?? "deploy/tofu");
const load = (relative) => readFile(path.join(root, relative), "utf8");
const files = {
  root: await load("official-cloud/main.tf"),
  versions: await load("official-cloud/versions.tf"),
  lock: await load("official-cloud/.terraform.lock.hcl"),
  locals: await load("modules/aws-cell/locals.tf"),
  outputs: await load("modules/aws-cell/outputs.tf"),
  network: await load("modules/aws-cell/network.tf"),
  eks: await load("modules/aws-cell/eks.tf"),
  database: await load("modules/aws-cell/database.tf"),
  cache: await load("modules/aws-cell/cache.tf"),
  storage: await load("modules/aws-cell/storage.tf"),
  secrets: await load("modules/aws-cell/secrets.tf"),
  recovery: await load("modules/aws-cell/recovery.tf"),
};

const failures = [];
const assert = (condition, message) => { if (!condition) failures.push(message); };
const occurrences = (contents, pattern) => [...contents.matchAll(pattern)].length;

assert(/backend\s+"s3"\s+\{\}/.test(files.versions), "official cloud must require a remote S3 backend");
assert(/version\s*=\s*"6\.54\.0"/.test(files.lock) && occurrences(files.lock, /"zh:[0-9a-f]{64}"/g) >= 10, "AWS provider lock is missing version or platform checksums");
assert(/module\s+"us"/.test(files.root) && /providers\s*=\s*\{\s*aws\s*=\s*aws\.us\s*\}/.test(files.root), "US cell provider isolation is missing");
assert(/module\s+"eu"/.test(files.root) && /providers\s*=\s*\{\s*aws\s*=\s*aws\.eu\s*\}/.test(files.root), "EU cell provider isolation is missing");
assert(/check\s+"cell_residency_isolation"/.test(files.root), "US/EU residency isolation check is missing");

assert(occurrences(files.network, /resource\s+"aws_subnet"\s+"(?:public|application|data)"/g) === 3, "three subnet tiers are required");
assert(/resource\s+"aws_nat_gateway"/.test(files.network) && /for_each\s*=\s*local\.az_map/.test(files.network), "per-AZ NAT gateways are missing");
assert(/resource\s+"aws_flow_log"/.test(files.network) && /traffic_type\s*=\s*"ALL"/.test(files.network), "all-direction VPC flow logs are missing");
assert(!/cidr_blocks\s*=\s*\["0\.0\.0\.0\/0"\]/.test(files.network), "security group allows unrestricted egress");

for (const [name, contents] of [["EKS", files.eks], ["Aurora", files.database], ["Valkey", files.cache], ["S3", files.storage], ["recovery", files.recovery]]) {
  assert(/lifecycle\s*\{\s*prevent_destroy\s*=\s*true\s*\}/s.test(contents), `${name} lacks a prevent_destroy boundary`);
}
assert(/endpoint_public_access\s*=\s*false/.test(files.eks) && /endpoint_private_access\s*=\s*true/.test(files.eks), "EKS API is not private-only");
assert(/enabled_cluster_log_types\s*=\s*\["api",\s*"audit",\s*"authenticator",\s*"controllerManager",\s*"scheduler"\]/.test(files.eks), "EKS control-plane audit logs are incomplete");
assert(/resources\s*=\s*\["secrets"\]/.test(files.eks) && /key_arn\s*=\s*aws_kms_key\.data\.arn/.test(files.eks), "EKS secret envelope encryption is missing");
assert(/min_size\s*=\s*var\.node_min_size/.test(files.eks) && /capacity_type\s*=\s*"ON_DEMAND"/.test(files.eks), "production node group scaling contract is missing");
assert(files.eks.includes('resource "aws_eks_access_entry" "administrator"') && /AmazonEKSClusterAdminPolicy/.test(files.eks), "EKS has no explicit administrator access entry");
assert(files.eks.includes('resource "aws_eks_addon" "ebs_csi"') && /depends_on\s*=\s*\[aws_eks_pod_identity_association\.ebs_csi\]/.test(files.eks), "EBS CSI add-on must wait for its Pod Identity association");
assert(files.eks.includes('resource "aws_iam_role_policy" "ebs_csi_kms"') && /kms:CreateGrant/.test(files.eks) && /kms:GrantIsForAWSResource/.test(files.eks), "EBS CSI lacks scoped KMS volume permissions");

assert(/count\s*=\s*3/.test(files.database), "Aurora must have three AZ-bound instances");
for (const field of ["storage_encrypted", "deletion_protection", "manage_master_user_password", "iam_database_authentication_enabled"]) assert(new RegExp(`${field}\\s*=\\s*true`).test(files.database), `Aurora ${field} is not enabled`);
assert(/backup_retention_period\s*=\s*35/.test(files.database), "Aurora retention is below 35 days");
assert(/num_cache_clusters\s*=\s*3/.test(files.cache) && /multi_az_enabled\s*=\s*true/.test(files.cache) && /transit_encryption_enabled\s*=\s*true/.test(files.cache) && /at_rest_encryption_enabled\s*=\s*true/.test(files.cache), "Valkey multi-AZ encryption contract is incomplete");

for (const requirement of ["aws_s3_bucket_public_access_block", "aws_s3_bucket_versioning", "aws_s3_bucket_server_side_encryption_configuration", "DenyInsecureTransport", "DenyWrongEncryption"]) assert(files.storage.includes(requirement), `S3 control missing: ${requirement}`);
assert(/force_destroy\s*=\s*false/.test(files.storage), "durable buckets may be force destroyed");

assert(/resource\s+"aws_dynamodb_table"\s+"store_epoch"/.test(files.recovery), "independent store epoch CAS table is missing");
for (const field of ["deletion_protection_enabled", "point_in_time_recovery", "server_side_encryption"]) assert(files.recovery.includes(field), `epoch anchor control missing: ${field}`);
assert(/aws_kms_key\.recovery\.arn/.test(files.recovery) && files.recovery.includes('resource "aws_iam_role" "recovery_operator"'), "recovery anchor is not isolated under its own KMS/operator boundary");
assert(/aws:MultiFactorAuthPresent/.test(files.recovery) && /dynamodb:TransactWriteItems/.test(files.recovery) && /dynamodb:LeadingKeys/.test(files.recovery), "epoch rotation role lacks MFA-scoped CAS permissions");
assert(/aws_backup_vault_lock_configuration/.test(files.recovery) && /enable_continuous_backup\s*=\s*true/.test(files.recovery), "locked continuous backup is missing");

assert(files.secrets.includes('resource "aws_eks_pod_identity_association" "external_secrets"'), "External Secrets does not use EKS Pod Identity");
assert(/local\.secret_components/.test(files.secrets) && /recovery_window_in_days\s*=\s*30/.test(files.secrets), "per-workload secret containers or recovery window are missing");
assert(files.locals.includes('"nats-server-tls"') && /output\s+"data_kms_key_arn"/.test(files.outputs), "NATS TLS secret container or data KMS output is missing");
assert(!Object.values(files).some((contents) => /(?:password|token|secret_string)\s*=\s*"[^"$]/i.test(contents)), "literal secret material is present in OpenTofu");

if (failures.length) {
  for (const failure of failures) console.error(failure);
  process.exit(1);
}
console.log("stage-2 tofu contract: cells=2 az_per_cell=3 eventstore=aurora valkey=encrypted epoch_anchor=dynamodb_cas status=passed");
