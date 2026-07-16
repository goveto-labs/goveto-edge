package nodes

import (
	"context"

	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

func loadNodeRelations(ctx context.Context, db *client.Client, node *model.Node, includeCache bool) error {
	addresses, err := db.NodeAddress.Query().
		Where(query.NodeAddress.NodeId.Equals(node.Id)).
		OrderBy(query.NodeAddress.CreatedAt.Asc()).
		Do(ctx)
	if err != nil {
		return err
	}
	dnsLines, err := db.NodeDNSLine.Query().
		Where(query.NodeDNSLine.NodeId.Equals(node.Id)).
		Do(ctx)
	if err != nil {
		return err
	}
	groups, err := db.NodeGroupMembership.Query().
		Where(query.NodeGroupMembership.NodeId.Equals(node.Id)).
		Do(ctx)
	if err != nil {
		return err
	}
	regions, err := db.NodeRegionMembership.Query().
		Where(query.NodeRegionMembership.NodeId.Equals(node.Id)).
		Do(ctx)
	if err != nil {
		return err
	}
	versions, err := db.NodeSiteConfigVersion.Query().
		Where(query.NodeSiteConfigVersion.NodeId.Equals(node.Id)).
		Do(ctx)
	if err != nil {
		return err
	}

	node.Addresses = nodeAddressPointers(addresses)
	node.DnsLines = nodeDNSLinePointers(dnsLines)
	node.GroupMemberships = nodeGroupMembershipPointers(groups)
	node.RegionMemberships = nodeRegionMembershipPointers(regions)
	node.SiteConfigVersions = nodeSiteConfigVersionPointers(versions)
	if includeCache {
		cache, err := db.NodeCacheConfig.FindUnique(
			ctx,
			query.NodeCacheConfig.NodeId.Equals(node.Id),
		)
		if err != nil {
			return err
		}
		node.CacheConfig = cache
		hardware, err := db.NodeHardwareProfile.FindUnique(
			ctx,
			query.NodeHardwareProfile.NodeId.Equals(node.Id),
		)
		if err != nil {
			return err
		}
		node.HardwareProfile = hardware
	}
	return nil
}

func nodeAddressPointers(values []model.NodeAddress) []*model.NodeAddress {
	result := make([]*model.NodeAddress, len(values))
	for index := range values {
		result[index] = &values[index]
	}
	return result
}

func nodeDNSLinePointers(values []model.NodeDNSLine) []*model.NodeDNSLine {
	result := make([]*model.NodeDNSLine, len(values))
	for index := range values {
		result[index] = &values[index]
	}
	return result
}

func nodeGroupMembershipPointers(values []model.NodeGroupMembership) []*model.NodeGroupMembership {
	result := make([]*model.NodeGroupMembership, len(values))
	for index := range values {
		result[index] = &values[index]
	}
	return result
}

func nodeRegionMembershipPointers(values []model.NodeRegionMembership) []*model.NodeRegionMembership {
	result := make([]*model.NodeRegionMembership, len(values))
	for index := range values {
		result[index] = &values[index]
	}
	return result
}

func nodeSiteConfigVersionPointers(values []model.NodeSiteConfigVersion) []*model.NodeSiteConfigVersion {
	result := make([]*model.NodeSiteConfigVersion, len(values))
	for index := range values {
		result[index] = &values[index]
	}
	return result
}
