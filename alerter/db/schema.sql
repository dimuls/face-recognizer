create table person (
    id bigserial primary key,
    name text not null
);

create table face (
    person_id bigint not null references person(id),
    descriptor real[128] not null unique
);