# Company context

* Domain: guardianintelligence.org (abbreviated in conversation with user as "gi.org")
* Repo ships specific products within the architecture. First major product: Postflight (reference Blacksmith.sh and verself)
* Stripe is payment rail only -- we don't use Stripe Subscriptions / Usage-Based Billing. We meter on our own (planned)
* Other useful reference architectures: Zarf/UDS, AWS Landing Zone Accelerator

<overall_strategy>
We are an open source reference architecture in addition to the Postflight core product. The value proposition for cloners:

1. We make release and deployment automation easy.
2. We make supply chain, network, and application security easy.
3. We make it easy to add integrations (Stripe, GitHub, and the like) securely.
4. We make disaster recovery easy.
5. We make monitoring easy: the system detects its own degradation, remediates what it can, and pages the human only when it can't. Nothing else pages the human.

We do all of this by gluing together excellent existing tools and letting the user focus on building and iterating on whatever their particular product is. The economics: bootstrap once onto powerful fixed-cost metal, then iterate at near-zero marginal cost until product-market fit — ideas are fragile before they are refined, so shipping the next refined version must be nearly free.
</overall_strategy>
