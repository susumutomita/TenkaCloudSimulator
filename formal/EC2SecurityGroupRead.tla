------------------------ MODULE EC2SecurityGroupRead ------------------------
CONSTANT IngressSets
VARIABLE result

Checks == [operation : {"DescribeSecurityGroups", "unsupported"},
           authorized : BOOLEAN, localRegion : BOOLEAN, instanceOnly : BOOLEAN,
           canonicalGroup : BOOLEAN, storedParent : BOOLEAN,
           parentClaim : BOOLEAN, parentDeclared : BOOLEAN, faithfulXML : BOOLEAN]
Init == result = {}

Read(f, current, template) ==
    /\ result = {}
    /\ f.operation = "DescribeSecurityGroups"
    /\ f.authorized /\ f.localRegion /\ ~f.instanceOnly
    /\ f.canonicalGroup
    /\ f.storedParent /\ f.parentClaim /\ f.parentDeclared
    /\ f.faithfulXML
    /\ result' = {[checks |-> f, current |-> current, template |-> template,
                   returned |-> current]}

Next == (\E f \in Checks, current, template \in IngressSets:
          Read(f, current, template)) \/ UNCHANGED result
TypeOK == result \subseteq [checks : Checks, current : IngressSets,
                            template : IngressSets, returned : IngressSets]
OperationAdmission == \A r \in result: r.checks.operation = "DescribeSecurityGroups"
AuthorizedRegionalRead == \A r \in result:
    r.checks.authorized /\ r.checks.localRegion /\ ~r.checks.instanceOnly
CanonicalGroup == \A r \in result: r.checks.canonicalGroup
CanonicalParent == \A r \in result:
    r.checks.storedParent /\ r.checks.parentClaim /\ r.checks.parentDeclared
CurrentIngress == \A r \in result: r.returned = r.current
FaithfulXML == \A r \in result: r.checks.faithfulXML
=============================================================================
