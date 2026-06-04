#!/usr/bin/env python3
# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.


import os
import re
import shutil
import time
import argparse
import logging
import asyncio
import sys
import uuid
import json
import functools
from typing import Tuple, Dict, List, Any
from dataclasses import dataclass


# Constants for environment variables
LOGGING_DIRECTORY_ENV = "LoggingDirectory"
CUSTOM_COVERAGE_DIRECTORY_ENV = "CustomCoverageDirectory"
TEST_RUN_ID_ENV = "TestRunId"
REGISTRY_ENV = "REGISTRY"
REPO_BASE_ENV = "REPO_BASE"
TAG_ENV = "TAG"
ADDITIONAL_GINKGO_FLAGS_ENV = "ADDITIONAL_GINKGO_FLAGS"
AKS_TEMPLATE_ENV = "AKS_TEMPLATE"
AKS_LOCATION_ENV = "AKS_LOCATION"

# Constants for file names
JUNIT_OUTPUT_FILE_NAME = "JUnit.xml"
SUPPORT_BUNDLE_DIR_NAME = "support-bundles"
COVERAGE_OUTPUT_FILE_NAME = "coverage.xml"
REPORT_OUTPUT_FILE_NAME = "report.xml"

# Constants for test environment variables
TEST_OUTPUT_ENV = "TEST_OUTPUT"
TEST_COVER_ENV = "TEST_COVER"
SKIP_BUILD_ENV = "SKIP_BUILD"
SUPPORT_BUNDLE_OUTPUT_DIR_ENV = "SUPPORT_BUNDLE_OUTPUT_DIR"
AKS_RESOURCE_GROUP_ENV = "AKS_RESOURCE_GROUP"
AKS_IS_TEST_ENV = "AKS_IS_TEST"
SKIP_CREATE_CLUSTER_ENV = "SKIP_CREATE_CLUSTER"
SKIP_SOCAT_ROLLBACK = "SKIP_SOCAT_ROLLBACK"
INSTALLATION_METHOD_ENV = "INSTALLATION_METHOD"


class EnvConfig:
    """
    Configuration class to encapsulate environment variables and their default
    values.
    """

    def __init__(self):
        self.logging_directory: str = os.getenv(
            LOGGING_DIRECTORY_ENV, os.getcwd())
        self.custom_coverage_directory: str = os.getenv(
            CUSTOM_COVERAGE_DIRECTORY_ENV, os.getcwd())
        self.run_id: str = os.getenv(
            TEST_RUN_ID_ENV, str(uuid.uuid4()))
        self.registry: str = os.getenv(REGISTRY_ENV, "")
        self.tag: str = os.getenv(TAG_ENV, "")
        self.repo_base: str = os.getenv(REPO_BASE_ENV, "")

    def __str__(self) -> str:
        return (
            f"EnvConfig(logging_directory={self.logging_directory}, "
            f"custom_coverage_directory={self.custom_coverage_directory}, "
            f"run_id={self.run_id}, "
            f"registry={self.registry}, "
            f"tag={self.tag}, "
            f"repo_base={self.repo_base})")


# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)sZ\t%(levelname)s\t%(message)s",
    datefmt="%Y-%m-%dT%H:%M:%S",
    style="%",
)


def move_existing_file(file_path: str) -> None:
    """
    Move an existing file to a new location with a timestamp.

    Args:
        file_path (str): The path of the file to move.
    """
    if os.path.exists(file_path):
        base, ext = os.path.splitext(file_path)
        new_file = f"{base}-{int(time.time())}-previous{ext}"
        logging.info(f"Moving existing {file_path} to {new_file}")
        shutil.move(file_path, new_file)


def prepare_directories(config: EnvConfig) -> Tuple[str, str, str]:
    """
    Prepare the logging and coverage directories and move existing files.

    Args:
        config (EnvConfig): The configuration object containing environment
        variables.

    Returns:
        tuple: Paths for JUnit output file, support bundle
        output directory, and coverage output file.
    """
    junit_output_file = os.path.join(
        config.logging_directory,
        JUNIT_OUTPUT_FILE_NAME)
    support_bundle_output_dir = os.path.join(
        config.logging_directory, SUPPORT_BUNDLE_DIR_NAME)
    coverage_output_file = os.path.join(
        config.custom_coverage_directory,
        COVERAGE_OUTPUT_FILE_NAME)

    logging.info(f"JUnit output file: {junit_output_file}")
    logging.info(
        f"Support bundle output directory: {support_bundle_output_dir}")
    logging.info(f"Coverage output file: {coverage_output_file}")

    files_to_check = [
        junit_output_file,
        coverage_output_file,
        support_bundle_output_dir]
    for file_path in files_to_check:
        move_existing_file(file_path)

    return (
        junit_output_file,
        support_bundle_output_dir,
        coverage_output_file,
    )


async def read_stream(stream: asyncio.StreamReader, logger: callable) -> None:
    """
    Read from a stream and log its output.

    Args:
        stream (asyncio.StreamReader): The stream to read from.
        log_func (function): The logging function to use.
    """
    while True:
        line = await stream.readline()
        if not line:
            break
        logger(line.decode().replace("\n", ""))


async def run_command(command: str, env: Dict[str, str],
                      quiet: bool = False) -> int:
    """
    Run the specified command asynchronously and handle its output.

    Args:
        command (str): The command to run.
        env (dict): The environment variables to use.
        quiet (bool): If True, suppress output to stderr/stdout.

    Returns:
        int: The exit code of the command.
    """
    process = await asyncio.create_subprocess_shell(
        command,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        env=env,
    )

    if quiet:
        await process.communicate()  # Discard output
    else:
        await asyncio.gather(
            read_stream(process.stdout, logging.info),
            read_stream(process.stderr, logging.error),
        )

    return await process.wait()


def copy_files(files_to_copy: Dict[str, str]) -> None:
    """
    Copy specified files to their destinations.

    Args:
        files_to_copy (dict): A dictionary with source files as keys and
        destination files as values.
    """
    for src, dest in files_to_copy.items():
        if os.path.exists(src):
            abs_src = os.path.abspath(src)
            abs_dest = os.path.abspath(dest)
            if abs_src == abs_dest:
                logging.info(f"Skipping copy for {src} they are the same")
                continue
            logging.info(f"Copying {src} to {dest}")
            if os.path.isdir(src):
                shutil.copytree(src, dest, dirs_exist_ok=True)
            else:
                shutil.copy(src, dest)


class Cluster:
    """
    Base class for cluster operations.
    """

    async def setup(self) -> None:
        """
        Set up the cluster.
        """
        raise NotImplementedError

    async def cleanup(self) -> None:
        """
        Clean up the cluster.
        """
        raise NotImplementedError


class KindCluster(Cluster):
    """
    Class for managing a kind cluster.
    """

    def __init__(self, nodes: str = "single", setup_vg: bool = False):
        if nodes not in ("single", "multi"):
            raise ValueError(f"Unsupported kind node config: {nodes}")
        self.nodes = nodes
        self.setup_vg = setup_vg

    async def setup(self) -> None:
        """
        Set up the kind cluster.
        """
        logging.info(f"Creating kind cluster (target=make {self.nodes})")
        if await run_command(f"make {self.nodes}", os.environ.copy()) != 0:
            logging.error("Failed to create kind cluster")
            raise RuntimeError("Failed to create kind cluster")
        if self.setup_vg:
            # Pre-create the LVM volume group the driver expects on each kind
            # node so tests can run without real NVMe hardware. The driver's
            # CreateVolume path short-circuits NVMe discovery when the VG
            # already exists.
            logging.info("Setting up LVM volume group on kind nodes")
            cmd = "make kind-setup-vg"
            if await run_command(cmd, os.environ.copy()) != 0:
                logging.error(
                    "Failed to set up LVM volume group on kind nodes"
                )
                raise RuntimeError(
                    "Failed to set up LVM volume group on kind nodes"
                )

    async def cleanup(self) -> None:
        """
        Clean up the kind cluster.
        """
        # we could cleanup the kind cluster here, but we don't need to
        logging.info("No cleanup needed for kind cluster")


class KwokCluster(Cluster):
    """
    Class for managing a kwok-tuned kind cluster used by the scale tests.

    The cluster is a single kind node with apiserver/etcd/cm/scheduler tuned
    for thousands of fake nodes. The kwok controller itself is installed by
    the test suite (via test/pkg/kwok), not here.
    """

    async def setup(self) -> None:
        """
        Set up the kwok-tuned kind cluster.
        """
        logging.info("Creating kwok cluster (target=make kwok-bootstrap)")
        if await run_command("make kwok-bootstrap", os.environ.copy()) != 0:
            logging.error("Failed to create kwok-tuned kind cluster")
            raise RuntimeError("Failed to create kwok-tuned kind cluster")

    async def cleanup(self) -> None:
        """
        Clean up the kwok-tuned kind cluster.
        """
        logging.info("No cleanup needed for kwok cluster")


class AksCluster(Cluster):
    """
    Class for managing an AKS cluster.
    """
    def __init__(self, cluster_template: str,
                 elasticsan_count: int,
                 config: EnvConfig):
        self.config = config
        if self.config.run_id == "":
            raise ValueError("Test Run ID is not set")
        if cluster_template == "":
            raise ValueError("AKS template is not set")
        if elasticsan_count < 0:
            raise ValueError("Elastic SAN count must be >= 0")

        self.cluster_template = cluster_template
        self.elasticsan_count = elasticsan_count
        self.aks_resource_group = f"lcd-{self.config.run_id}"

    async def setup(self) -> None:
        """
        Set up the AKS cluster.
        """
        requirements = await self.get_cluster_requirements()

        def has_regions(regions):
            return len(regions) > 0

        # Use the retry utility
        regions = await retry_async(
            functools.partial(self.get_best_region, requirements),
            retries=3,
            delay=10,
            validator=has_regions,
            error_msg="No available regions found after multiple attempts"
        )

        logging.info(f"Best region for AKS cluster: {regions[0]}")
        logging.info(f"Creating AKS cluster in region: {regions[0]}")
        await self.create_aks_cluster(regions[0])

    async def get_cluster_requirements(self) -> Dict[str, Any]:
        """
        Get the cluster requirements from the template.

        Returns:
            dict: The cluster requirements.
        """
        cmd_env = os.environ.copy()
        cmd_env[AKS_RESOURCE_GROUP_ENV] = self.aks_resource_group
        cmd_env[AKS_IS_TEST_ENV] = "true"
        cmd_env[AKS_TEMPLATE_ENV] = self.cluster_template

        logging.info("Determining cluster requirements from template...")
        process = await asyncio.create_subprocess_shell(
            "make aks-cluster-requirements",
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            env=cmd_env
        )
        stdout, stderr = await process.communicate()

        if process.returncode != 0:
            logging.error("Failed to get cluster requirements:"
                          f"{stderr.decode().strip()}")
            raise RuntimeError("Failed to get cluster requirements")

        json_match = re.search(
            r'Requirements:\s*(\{.*\})',
            stdout.decode().strip(), re.DOTALL)

        if not json_match:
            logging.error("Failed to parse cluster requirements")
            raise ValueError("Failed to parse cluster requirements")

        requirements_match_group = json_match.group(1)
        requirements = json.loads(requirements_match_group)
        logging.info(
            f"Using template-provided values: "
            f"nodeCount={requirements.get('nodeCount')}, "
            f"vmSku={requirements.get('vmSku')}, "
            f"availabilityZones={requirements.get('availabilityZones')}"
        )
        return requirements

    async def get_best_region(self, requirements: Dict[str, Any]) -> List[str]:
        """
        Find the best Azure region based on the cluster requirements.

        Args:
            requirements (dict): The cluster requirements.

        Returns:
            list: The list of best regions.
        """
        actual_node_count = requirements.get("nodeCount")
        actual_vm_sku = requirements.get("vmSku")
        actual_zones = requirements.get("availabilityZones")

        regions = await select_azure_region(
            actual_node_count,
            actual_vm_sku,
            self.elasticsan_count,
            actual_zones
        )

        return regions

    async def create_aks_cluster(self, region: str) -> None:
        """
        Create the AKS cluster in the specified region.

        Args:
            region (str): The region to create the cluster in.
        """
        env = os.environ.copy()
        env[AKS_RESOURCE_GROUP_ENV] = self.aks_resource_group
        env[AKS_IS_TEST_ENV] = "true"
        env[AKS_TEMPLATE_ENV] = self.cluster_template
        env[AKS_LOCATION_ENV] = region

        logging.info("Creating AKS cluster: "
                     f"{self.aks_resource_group} in {region}")

        if await run_command("make aks", env) != 0:
            logging.error("Failed to create AKS cluster")
            raise RuntimeError("Failed to create AKS cluster")

    async def cleanup(self) -> None:
        """
        Clean up the AKS cluster.
        """
        env = os.environ.copy()
        env[AKS_RESOURCE_GROUP_ENV] = self.aks_resource_group
        env[AKS_TEMPLATE_ENV] = self.cluster_template
        logging.info(f"Cleaning AKS cluster: {self.aks_resource_group}")
        if await run_command("make aks-clean", env) != 0:
            logging.error("Failed to clean AKS cluster")
            raise RuntimeError("Failed to clean AKS cluster")

    def get_aks_cluster_resource_group(self) -> str:
        """
        Get the AKS cluster resource group.

        Returns:
            str: The AKS cluster resource group.
        """
        return f"lcd-{self.config.run_id}"


class NoCluster(Cluster):
    """
    Class for managing no cluster.
    """

    async def setup(self) -> None:
        """
        Skip cluster setup.
        """
        logging.info("Skipping cluster setup")

    async def cleanup(self) -> None:
        """
        No cleanup needed for no cluster.
        """
        logging.info("No cleanup needed for no cluster")


def create_cluster(args: argparse.Namespace, config: EnvConfig) -> Cluster:
    """
    Factory method to create a cluster instance based on the cluster type.

    Args:
        args (argparse.Namespace): The parsed command-line arguments.
        config (EnvConfig): The configuration object containing environment
            variables.

    Returns:
        Cluster: An instance of the appropriate cluster class.
    """
    if args.cluster_type == "kind":
        return KindCluster(nodes=args.kind_nodes, setup_vg=args.kind_setup_vg)
    elif args.cluster_type == "kwok":
        return KwokCluster()
    elif args.cluster_type == "aks":
        return AksCluster(
            args.aks_template,
            args.required_elastic_sans,
            config
        )
    elif args.cluster_type == "none":
        return NoCluster()
    else:
        raise ValueError(f"Unknown cluster type: {args.cluster_type}")


async def run_dry_run(command: str) -> None:
    """
    Run the specified command in dry-run mode.

    Args:
        command (str): The command to run.
        env (dict): The environment variables to use.
    """
    env = {
        **os.environ,
        ADDITIONAL_GINKGO_FLAGS_ENV: "--dry-run",
    }
    if await run_command(command, env, quiet=True) != 0:
        logging.error("Dry-run failed")
        raise RuntimeError("Dry-run failed")


async def retry_async(func, retries, delay, validator, error_msg):
    """Retry an async function with static delay"""
    for attempt in range(1, retries + 1):
        try:
            logging.info(f"Attempt {attempt}/{retries}:")
            result = await func()

            if not validator(result):
                if attempt == retries:
                    raise ValueError(error_msg)
                logging.warning(
                    f"Attempt {attempt}: validation failed, retrying...")
            else:
                return result

        except Exception as e:
            if attempt == retries:
                logging.error(f"Failed after {retries} attempts: {str(e)}")
                raise

            logging.warning(f"Attempt {attempt} failed: {str(e)}")

        # Only sleep if we're going to retry
        if attempt < retries:
            logging.info(f"Retrying in {delay}s...")
            await asyncio.sleep(delay)


async def main() -> int:
    """
    Main function to run the script.
    """
    logging.info("Starting script execution")

    parser = argparse.ArgumentParser(
        description="Run a command and generate a JUnit report."
    )
    parser.add_argument("--command", required=True, help="Command to run")
    parser.add_argument(
        "--run-dry-run",
        action="store_true",
        help="Run the command in dry-run mode during cluster-create",
    )
    parser.add_argument(
        "--cluster-type",
        choices=["kind", "kwok", "aks", "none"],
        default="none",
        required=False,
        help="Specify the cluster type: kind, aks, or none",
    )

    parser.add_argument(
        "--kind-nodes",
        type=str,
        choices=["single", "multi"],
        default="single",
        required=False,
        help=(
            "Kind cluster configuration: single (1 node) or multi (3 nodes). "
            "Only used when --cluster-type=kind."
        ),
    )

    parser.add_argument(
        "--kind-setup-vg",
        action="store_true",
        help=(
            "After creating the kind cluster, pre-create the LVM volume "
            "group the driver expects on each node. "
            "Only used when --cluster-type=kind."
        ),
    )

    parser.add_argument(
        "--aks-template",
        default="nvme",
        required=False,
        help="Specify the AKS template to use",
    )

    parser.add_argument(
        "--required-elastic-sans",
        type=int,
        default=0,
        required=False,
        help="Specify the number of required elastic SANs",
    )

    args = parser.parse_args()

    command_to_run = args.command

    config = EnvConfig()

    logging.info(f"environment variables: {config}")

    junit_file, support_dir, cover_out_file = \
        prepare_directories(config)

    cluster = create_cluster(args, config)

    setups_ops = [
        cluster.setup()
    ]

    if args.run_dry_run:
        setups_ops.append(run_dry_run(command_to_run))

    await asyncio.gather(*setups_ops)

    env = {
        **os.environ,
        TEST_OUTPUT_ENV: REPORT_OUTPUT_FILE_NAME,
        TEST_COVER_ENV: COVERAGE_OUTPUT_FILE_NAME,
        SKIP_BUILD_ENV: "true",
        SKIP_CREATE_CLUSTER_ENV: "true",
        SKIP_SOCAT_ROLLBACK: "true",
        SUPPORT_BUNDLE_OUTPUT_DIR_ENV: SUPPORT_BUNDLE_DIR_NAME,
        INSTALLATION_METHOD_ENV: "helm",
    }

    exit_code = await run_command(command_to_run, env)
    logging.info(f"Command exited with code: {exit_code}")

    files_to_copy = {
        REPORT_OUTPUT_FILE_NAME: junit_file,
        COVERAGE_OUTPUT_FILE_NAME: cover_out_file,
        SUPPORT_BUNDLE_DIR_NAME: support_dir,
    }

    copy_files(files_to_copy)

    logging.info("Script execution completed")

    await cluster.cleanup()

    return exit_code


@dataclass
class RegionAvailability:
    """Encapsulates the availability metrics for a specific Azure region."""
    zone_availability: float = 0.0
    elastic_san_usage: int = 0
    vm_usage: Dict[str, Dict[str, Any]] = None

    def __post_init__(self):
        if self.vm_usage is None:
            self.vm_usage = {}


async def _run_azure_cli(cmd: List[str], timeout: int = 180) -> str:
    """
    Run an Azure CLI command asynchronously.

    Args:
        cmd: List of command arguments
        timeout: Timeout in seconds

    Returns:
        Command output as text

    Raises:
        RuntimeError: If command fails or times out
    """
    process = await asyncio.create_subprocess_exec(
        "az",
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE
    )

    try:
        stdout, stderr = await asyncio.wait_for(process.communicate(), timeout)

        if process.returncode != 0:
            error_msg = stderr.decode().strip()
            raise RuntimeError(f"Azure CLI command failed: {error_msg}")

        return stdout.decode()

    except asyncio.TimeoutError:
        process.kill()
        raise RuntimeError(
            f"Azure CLI command timed out after {timeout} seconds"
        )


async def select_azure_region(
    node_count: int,
    node_vm_size: str,
    required_elastic_sans: int,
    node_availability_zones: List[str]
) -> List[str]:
    """
    Select the best Azure regions based on resource availability.
    """
    MAX_ZONES_PER_REGION = 3
    MAX_ELASTIC_SANS_PER_REGION = 20

    # Use consistent regions list
    eligible_regions = [
        "eastus", "northeurope", "westus3", "uksouth", "australiaeast",
        "westus2", "westeurope", "swedencentral", "southeastasia",
        "francecentral", "southcentralus", "eastus2"
    ]

    # Parse VM size info
    required_family, required_cores = _parse_vm_size(node_vm_size, node_count)
    required_cpus_by_family = {required_family: required_cores}

    # Initialize region data structure
    regions = {region: RegionAvailability() for region in eligible_regions}

    # Get VM availability restrictions
    await _collect_vm_size_availability(
        regions,
        node_vm_size,
        node_availability_zones,
        MAX_ZONES_PER_REGION
    )

    # Get elastic SAN usage if needed
    if required_elastic_sans > 0:
        await _install_elastic_san_extension()
        await _collect_elastic_san_usage(regions)

    # Get VM usage per region
    await _collect_vm_usage(regions, eligible_regions)

    # Calculate requirements
    vm_requirements = _calculate_vm_requirements(required_cpus_by_family)

    # Calculate and sort availability scores
    return _calculate_availability_scores(
        regions,
        vm_requirements,
        required_elastic_sans,
        MAX_ELASTIC_SANS_PER_REGION,
        eligible_regions
    )


def _parse_vm_size(vm_size: str, node_count: int) -> tuple:
    """Parse VM size to determine CPU family and cores."""
    vm_size_pattern = r'^\D+_(\D+)(\d+)(?:-\d+)?(\D*)_v(\d+)$'
    match = re.match(vm_size_pattern, vm_size)
    if not match:
        raise ValueError(f"Failed to parse Azure VM size: {vm_size}")

    required_family = f"{match.group(1)}{match.group(3)}v{match.group(4)}"
    required_family = required_family.lower()
    required_cores = node_count * int(match.group(2))

    logging.info(
        f"Required family: {required_family}, "
        f"required CPU cores: {required_cores}"
    )
    return required_family, required_cores


async def _collect_vm_size_availability(
        regions: Dict[str, RegionAvailability],
        vm_size: str,
        node_availability_zones: List[str],
        max_zones: int) -> None:
    """Collect VM size availability data for each region."""
    logging.info(
        f"Retrieving Azure region and zone restrictions "
        f"for VM size: {vm_size}..."
    )

    try:
        cmd = [
            "vm", "list-skus",
            "--resource-type", "virtualMachines",
            "--size", vm_size,
            "--query",
            (
                "[].{Location: locations[0], "
                "RestrictedZones: restrictions[].restrictionInfo.zones[]}"
            )
        ]
        vm_skus_json = await _run_azure_cli(cmd, timeout=300)
        vm_skus = json.loads(vm_skus_json)
        node_az_set = set(node_availability_zones)

        for item in vm_skus:
            location = item.get("Location", "").lower()
            if location not in regions:
                continue

            restricted_zones = item.get("RestrictedZones", []) or []

            # Calculate zone availability
            has_restricted_zones = bool(
                node_az_set.intersection(restricted_zones)
            )
            zone_availability = (1.0 - int(has_restricted_zones)) * (
                1.0 - len(restricted_zones) / max_zones
            )
            regions[location].zone_availability = zone_availability

            logging.info(
                f"Region: {location}, "
                f"Availability Zones: {node_availability_zones}, "
                f"Restricted Zones: {restricted_zones}, "
                f"Zone Availability: {zone_availability:.0%}"
            )
    except Exception as e:
        logging.error(f"Error getting VM size availability: {str(e)}")
        # Set default availability to zero
        for region in regions:
            regions[region].zone_availability = 0.0


async def _collect_elastic_san_usage(
        regions: Dict[str, RegionAvailability]) -> None:
    """Collect Elastic SAN usage information."""
    logging.info("Retrieving Azure Elastic SAN usage...")

    try:
        cmd = [
            "elastic-san", "list",
            "--query", "[].location",
            "--output", "tsv"
        ]
        elastic_sans_output = await _run_azure_cli(cmd)
        elastic_sans_output = elastic_sans_output.strip()

        if elastic_sans_output:
            for location in elastic_sans_output.split("\n"):
                location = location.lower()
                if location in regions:
                    regions[location].elastic_san_usage += 1

        logging.info("elastic_san_usage: ")
        for region, data in regions.items():
            logging.info(
                f"Region: {region}, "
                f"Elastic SAN Usage: {data.elastic_san_usage}"
            )

    except Exception as e:
        logging.error(f"Error getting elastic SAN usage: {str(e)}")


async def _collect_vm_usage(
        regions: Dict[str, RegionAvailability],
        eligible_regions: List[str]) -> None:
    """Collect VM usage information for each region in parallel."""
    logging.info("Retrieving Azure VM usage...")

    async def fetch_region_usage(region: str) -> tuple:
        """Fetch usage for a single region and return (region, data)."""
        try:
            cmd = [
                "vm", "list-usage",
                "--location", region,
                "--query",
                r"[].{Current: currentValue, Max: limit, Name: name.value}",
                "--output", "json"
            ]
            vm_usage_json = await _run_azure_cli(cmd, timeout=300)
            vm_usage_list = json.loads(vm_usage_json)

            # Create case-insensitive lookup dictionary
            return (
                region,
                {item["Name"].lower(): item for item in vm_usage_list},
            )
        except Exception as e:
            logging.error("Failed to get VM usage for region"
                          f"{region}: {str(e)}")
            return region, {}

    # Create tasks for all regions
    tasks = [
        fetch_region_usage(region) for region in eligible_regions
    ]

    # Wait for all tasks to complete
    results = await asyncio.gather(*tasks)

    # Update regions with results
    for region, usage_data in results:
        if region in regions:
            regions[region].vm_usage = usage_data


def _calculate_vm_requirements(
    required_cpus_by_family: Dict[str, int]
) -> Dict[str, int]:
    """Calculate VM requirements from CPU requirements."""
    vm_requirements = {}

    for family, cores in required_cpus_by_family.items():
        vm_requirements["cores"] = vm_requirements.get("cores", 0) + cores
        vm_requirements[f"standard{family}family"] = cores

    return vm_requirements


def _calculate_availability_scores(
    regions: Dict[str, RegionAvailability],
    vm_requirements: Dict[str, int],
    required_elastic_sans: int,
    max_elastic_sans: int,
    eligible_regions: List[str]
) -> List[str]:
    """Calculate availability scores and return sorted regions."""
    availability_factor = 1.0
    scores = {}

    for region in eligible_regions:
        region_data = regions[region]

        # Calculate elastic SAN availability
        esan_avail = 1.0 - (
            required_elastic_sans + region_data.elastic_san_usage
        ) / max_elastic_sans

        # Calculate overall availability
        overall_availability = (
            max(0.0, esan_avail) * max(0.0, region_data.zone_availability)
        )

        # Check resource requirements
        for req_name, req_value in vm_requirements.items():
            resource_usage = region_data.vm_usage.get(
                req_name, {"Current": 0, "Max": 1}
            )

            current_val = float(resource_usage.get("Current", 0))
            max_val = float(resource_usage.get("Max", 1))

            resource_availability = 1.0 - (current_val + req_value) / max_val
            overall_availability *= max(0.0, resource_availability)

        # Calculate final availability score
        avail_score = overall_availability * availability_factor

        logging.info(
            f"{region:16} availability score: {avail_score:.2%} "
            f"elastic san availability: {esan_avail:.0%}, "
            f"zone availability: {region_data.zone_availability:.0%}"
        )

        if avail_score > 0:
            scores[region] = avail_score
            availability_factor *= 0.85  # Prefer earlier regions in the list

    if not scores:
        raise ValueError("Failed to find an available region.")

    # Return regions sorted by score
    return [
        region for region, _ in sorted(
            scores.items(), key=lambda x: x[1], reverse=True
        )
    ]


async def _install_elastic_san_extension() -> None:
    """
    Install the Azure Elastic SAN extension if not already installed.
    """
    logging.info("Installing Azure Elastic SAN extension...")

    try:
        cmd = ["extension", "add", "-n", "elastic-san", "--yes"]
        await _run_azure_cli(cmd)
        logging.info("Successfully installed Azure Elastic SAN extension")
    except RuntimeError as e:
        # Check if it failed because the extension is already installed
        if "already installed" in str(e).lower():
            logging.info("Azure Elastic SAN extension is already installed")
        else:
            logging.error(f"Error installing extension: {str(e)}")
            raise RuntimeError(f"Failed to install extension: {str(e)}")

if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
